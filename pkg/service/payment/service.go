package payment

import (
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/fritzpay/paymentd/pkg/paymentd/payment"
	"github.com/fritzpay/paymentd/pkg/paymentd/payment_method"
	"github.com/fritzpay/paymentd/pkg/paymentd/project"
	"github.com/fritzpay/paymentd/pkg/server"
	"github.com/fritzpay/paymentd/pkg/service"
	"github.com/go-sql-driver/mysql"
	"gopkg.in/inconshreveable/log15.v2"
)

type errorID int

func (e errorID) Error() string {
	switch e {
	case ErrDB:
		return "database error"
	case ErrDBLockTimeout:
		return "lock wait timeout"
	case ErrDuplicateIdent:
		return "duplicate ident in payment"
	case ErrPaymentCallbackConfig:
		return "callback config error"
	case ErrPaymentMethodNotFound:
		return "payment method not found"
	case ErrPaymentMethodConflict:
		return "payment method project mismatch"
	case ErrPaymentMethodInactive:
		return "payment method inactive"
	case ErrPaymentMethodDisabled:
		return "payment method disabled"
	case ErrInternal:
		return "internal error"
	case ErrIntentTimeout:
		return "intent timeout"
	case ErrIntentNotAllowed:
		return "intent not allowed"
	default:
		return "unknown error"
	}
}

const (
	// general database error
	ErrDB errorID = iota
	// lock wait timeout
	ErrDBLockTimeout
	// duplicate Ident in payment
	ErrDuplicateIdent
	// callback config error
	ErrPaymentCallbackConfig
	// payment method not found
	ErrPaymentMethodNotFound
	// payment method project mismatch
	ErrPaymentMethodConflict
	// payment method inactive
	ErrPaymentMethodInactive
	// payment method disabled
	ErrPaymentMethodDisabled
	// internal error
	ErrInternal
	// intent timeout
	ErrIntentTimeout
	// intent not allowed
	ErrIntentNotAllowed
)

const (
	notificationBufferSize = 16
	commitIntentTimeout    = time.Minute
)

const (
	// PaymentTokenMaxAgeDefault is the default maximum age of payment tokens
	PaymentTokenMaxAgeDefault = time.Minute * 15
	// PaymentTokenParam is the name of the token parameter
	PaymentTokenParam = "token"
)

// IntentWorkers are the primary means of synchronizing and controlling changes on payment
// states.
//
// IntentWorkers are registered with the payment service via the Service.RegiserIntentWorker
// method.
//
// Whenever another service or process wishes to change the state of a payment, it should
// do so by invoking one of the Intent* methods. These methods will create the
// matching PaymentTransaction types and start the intent procedure.
//
// PreIntent is invoked prior to the intent creation. Any errors sent through the res channel
// will cancel the intent procedure and the calling service will receive the first
// encountered error. Once the done channel is closed, the intent procedure won't accept any
// results of the IntentWorker anymore. This is usually due to timeout.
type PreIntentWorker interface {
	PreIntent(p payment.Payment, paymentTx payment.PaymentTransaction, done <-chan struct{}, res chan<- error)
}

// PostIntentWorker are invoked concurrently right before the Intent* methods will return the
// matching Transaction. At this point the intent cannot be cancelled. Any errors sent
// through the returned channel will be logged.
type PostIntentWorker interface {
	PostIntent(p payment.Payment, paymentTx payment.PaymentTransaction) <-chan error
}

// CommitIntentWorker are invoked when the intent is committed through the returned
// CommitIntentFunc. The intended state change is considered committed and subsequent
// actions can be taken.
//
// The default Payment Service has the notification registered as a default
// commit intent worker.
type CommitIntentWorker interface {
	CommitIntent(paymentTx *payment.PaymentTransaction) error
}

type CommitIntentFunc func()

// Service is the payment service
type Service struct {
	ctx *service.Context
	log log15.Logger

	idCoder *payment.IDEncoder

	tr *http.Transport
	cl *http.Client

	mIntent       sync.RWMutex
	preIntents    []PreIntentWorker
	postIntents   []PostIntentWorker
	commitIntents []CommitIntentWorker
}

// NewService creates a new payment service
func NewService(ctx *service.Context) (*Service, error) {
	s := &Service{
		ctx: ctx,
		log: ctx.Log().New(log15.Ctx{
			"pkg": "github.com/fritzpay/paymentd/pkg/service/payment",
		}),

		preIntents:    make([]PreIntentWorker, 0, 16),
		postIntents:   make([]PostIntentWorker, 0, 16),
		commitIntents: make([]CommitIntentWorker, 0, 16),
	}

	var err error
	cfg := ctx.Config()

	s.idCoder, err = payment.NewIDEncoder(cfg.Payment.PaymentIDEncPrime, cfg.Payment.PaymentIDEncXOR)
	if err != nil {
		s.log.Error("error initializing payment ID encoder", log15.Ctx{"err": err})
		return nil, err
	}

	s.tr = &http.Transport{}
	s.cl = &http.Client{
		Transport: s.tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 {
				return errors.New("too many redirects")
			}
			// keep user-agent
			if len(via) > 0 {
				lastReq := via[len(via)-1]
				if lastReq.Header.Get("User-Agent") != "" {
					req.Header.Set("User-Agent", lastReq.Header.Get("User-Agent"))
				}
			}
			return nil
		},
	}

	s.RegisterCommitIntentWorker(&intentNotify{s})

	go s.handleBackground()

	return s, nil
}

func (s *Service) handleBackground() {
	// if attached to a server, this will tell the server to wait with shutting down
	// until the cleanup process is complete
	server.Wait.Add(1)
	defer server.Wait.Done()
	for {
		select {
		case <-s.ctx.Done():
			s.log.Info("service context closed", log15.Ctx{"err": s.ctx.Err()})
			s.log.Info("closing idle connections...")
			s.tr.CloseIdleConnections()
			return
		}
	}
}

func (s *Service) RegisterPreIntentWorker(worker PreIntentWorker) {
	s.mIntent.Lock()
	s.preIntents = append(s.preIntents, worker)
	s.mIntent.Unlock()
}

func (s *Service) RegisterPostIntentWorker(worker PostIntentWorker) {
	s.mIntent.Lock()
	s.postIntents = append(s.postIntents, worker)
	s.mIntent.Unlock()
}

func (s *Service) RegisterCommitIntentWorker(worker CommitIntentWorker) {
	s.mIntent.Lock()
	s.commitIntents = append(s.commitIntents, worker)
	s.mIntent.Unlock()
}

// EncodedPaymentID returns a payment id with the id part encoded
func (s *Service) EncodedPaymentID(id payment.PaymentID) payment.PaymentID {
	id.PaymentID = s.idCoder.Hide(id.PaymentID)
	return id
}

// DecodedPaymentID returns a payment id with the id part decoded
func (s *Service) DecodedPaymentID(id payment.PaymentID) payment.PaymentID {
	id.PaymentID = s.idCoder.Show(id.PaymentID)
	return id
}

// CreatePayment creates a new payment
func (s *Service) CreatePayment(tx *sql.Tx, p *payment.Payment) error {
	log := s.log.New(log15.Ctx{
		"method": "CreatePayment",
	})
	if p.Config.HasCallback() {
		callbackProjectKey, err := project.ProjectKeyByKeyDB(s.ctx.PrincipalDB(service.ReadOnly), p.Config.CallbackProjectKey.String)
		if err != nil {
			if err == project.ErrProjectKeyNotFound {
				log.Error("callback project key not found", log15.Ctx{"callbackProjectKey": p.Config.CallbackProjectKey.String})
				return ErrPaymentCallbackConfig
			}
			log.Error("error retrieving callback project key", log15.Ctx{"err": err})
			return ErrDB
		}
		if callbackProjectKey.Project.ID != p.ProjectID() {
			log.Error("callback project mismatch", log15.Ctx{
				"callbackProjectKey": callbackProjectKey.Key,
				"callbackProjectID":  callbackProjectKey.Project.ID,
				"projectID":          p.ProjectID(),
			})
			return ErrPaymentCallbackConfig
		}
	}
	err := payment.InsertPaymentTx(tx, p)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			if mysqlErr.Number == 1213 {
				return ErrDBLockTimeout
			}
		}
		_, existErr := payment.PaymentByProjectIDAndIdentTx(tx, p.ProjectID(), p.Ident)
		if existErr != nil && existErr != payment.ErrPaymentNotFound {
			log.Error("error on checking duplicate ident", log15.Ctx{"err": err})
			return ErrDB
		}
		// payment found => duplicate error
		if existErr == nil {
			return ErrDuplicateIdent
		}
		log.Error("error on insert payment", log15.Ctx{"err": err})
		return ErrDB
	}
	err = s.SetPaymentConfig(tx, p)
	if err != nil {
		return err
	}
	err = s.SetPaymentMetadata(tx, p)
	if err != nil {
		return err
	}
	return nil
}

// SetPaymentConfig sets/updates the payment configuration
func (s *Service) SetPaymentConfig(tx *sql.Tx, p *payment.Payment) error {
	log := s.log.New(log15.Ctx{"method": "SetPaymentConfig"})
	if p.Config.PaymentMethodID.Valid {
		log = log.New(log15.Ctx{"paymentMethodID": p.Config.PaymentMethodID.Int64})
		meth, err := payment_method.PaymentMethodByIDTx(tx, p.Config.PaymentMethodID.Int64)
		if err != nil {
			if mysqlErr, ok := err.(*mysql.MySQLError); ok {
				if mysqlErr.Number == 1213 {
					return ErrDBLockTimeout
				}
			}
			if err == payment_method.ErrPaymentMethodNotFound {
				log.Warn(ErrPaymentMethodNotFound.Error())
				return ErrPaymentMethodNotFound
			}
			log.Error("error on select payment method", log15.Ctx{"err": err})
			return ErrDB
		}
		if meth.ProjectID != p.ProjectID() {
			log.Warn(ErrPaymentMethodConflict.Error())
			return ErrPaymentMethodConflict
		}
		if !meth.Active() {
			log.Warn(ErrPaymentMethodInactive.Error())
			return ErrPaymentMethodInactive
		}
	}
	err := payment.InsertPaymentConfigTx(tx, p)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			if mysqlErr.Number == 1213 {
				return ErrDBLockTimeout
			}
		}
		log.Error("error on insert payment config", log15.Ctx{"err": err})
		return ErrDB
	}
	return nil
}

// SetPaymentMetadata sets/updates the payment metadata
func (s *Service) SetPaymentMetadata(tx *sql.Tx, p *payment.Payment) error {
	log := s.log.New(log15.Ctx{"method": "SetPaymentMetadata"})
	// payment metadata
	if p.Metadata == nil {
		return nil
	}
	err := payment.InsertPaymentMetadataTx(tx, p)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			if mysqlErr.Number == 1213 {
				return ErrDBLockTimeout
			}
		}
		log.Error("error on insert payment metadata", log15.Ctx{"err": err})
		return ErrDB
	}
	return nil
}

// IsProcessablePayment returns true if the given payment is considered processable
//
// All required fields are present.
func (s *Service) IsProcessablePayment(p *payment.Payment) bool {
	if !p.Config.IsConfigured() {
		return false
	}
	if !p.Config.Country.Valid {
		return false
	}
	if !p.Config.Locale.Valid {
		return false
	}
	if !p.Config.PaymentMethodID.Valid {
		return false
	}
	return true
}

// IsInitialized returns true when the payment is in a processing state, i.e.
// when there is at least one transaction present
func (s *Service) IsInitialized(p *payment.Payment) bool {
	return p.Status != payment.PaymentStatusNone
}

// SetPaymentTransaction adds a new payment transaction
//
// If a callback method is configured for this payment/project, it will send a callback
// notification
func (s *Service) SetPaymentTransaction(tx *sql.Tx, paymentTx *payment.PaymentTransaction) error {
	log := s.log.New(log15.Ctx{"method": "SetPaymentTransaction"})
	err := payment.InsertPaymentTransactionTx(tx, paymentTx)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			if mysqlErr.Number == 1213 {
				return ErrDBLockTimeout
			}
		}
		log.Error("error saving payment transaction", log15.Ctx{"err": err})
		return ErrDB
	}
	return nil
}

// PaymentTransaction returns the current payment transaction for the given payment
//
// PaymentTransaction will return a payment.ErrPaymentTransactionNotFound if no such
// transaction exists (i.e. the payment is uninitialized)
func (s *Service) PaymentTransaction(tx *sql.Tx, p *payment.Payment) (*payment.PaymentTransaction, error) {
	return payment.PaymentTransactionCurrentTx(tx, p)
}

func (s *Service) handleIntent(
	p *payment.Payment,
	paymentTx *payment.PaymentTransaction,
	timeout time.Duration) (*payment.PaymentTransaction, CommitIntentFunc, error) {

	if deadline, ok := s.ctx.Deadline(); ok {
		if time.Now().Add(timeout).After(deadline) {
			return nil, nil, ErrIntentTimeout
		}
	}

	// no-op
	var commitFunc CommitIntentFunc

	s.mIntent.RLock()
	if len(s.preIntents) > 0 {
		// pre-intent
		done := make(chan struct{})
		c := make(chan error, 1)
		for _, w := range s.preIntents {
			// run all preintents in goroutines
			go w.PreIntent(*p, *paymentTx, done, c)
		}
		// wait
		select {
		// context cancelled
		case <-s.ctx.Done():
			close(done)
			s.mIntent.RUnlock()
			return nil, nil, s.ctx.Err()

		// error received
		case err := <-c:
			close(done)
			s.mIntent.RUnlock()
			return nil, nil, err

		// continue
		case <-time.After(timeout):
			close(done)
		}
	}

	if len(s.postIntents) > 0 {
		// post-intent
		postDone := make([]<-chan error, len(s.postIntents))
		for i, w := range s.postIntents {
			postDone[i] = w.PostIntent(*p, *paymentTx)
		}
		go func(wait []<-chan error) {
			var wg sync.WaitGroup
			for _, w := range wait {
				wg.Add(1)
				go func(c <-chan error) {
					err, ok := <-c
					if ok && err != nil {
						s.log.Warn("error on post intent action", log15.Ctx{
							"intent": paymentTx.Status.String(),
							"err":    err,
						})
					}
					wg.Done()
				}(w)
			}
			wg.Wait()
		}(postDone)
	}

	if len(s.commitIntents) > 0 {
		// commit intent
		commit := make(chan struct{})
		commitFunc = CommitIntentFunc(func() {
			close(commit)
		})
		errC := make(chan error)
		go func() {
			select {
			case <-commit:
				var wg sync.WaitGroup
				s.mIntent.RLock()
				for _, w := range s.commitIntents {
					wg.Add(1)
					go func(w CommitIntentWorker) { errC <- w.CommitIntent(paymentTx) }(w)
				}
				s.mIntent.RUnlock()
				go func() {
					for {
						err, ok := <-errC
						if !ok {
							return
						}
						wg.Done()
						if err != nil {
							s.log.Warn("error on commit intent action", log15.Ctx{
								"intent": paymentTx.Status.String(),
								"err":    err,
							})
						}
					}
				}()
				wg.Wait()
			case <-time.After(commitIntentTimeout):
			}
		}()
	}
	s.mIntent.RUnlock()

	return paymentTx, commitFunc, nil
}

func (s *Service) IntentOpen(p *payment.Payment, timeout time.Duration) (*payment.PaymentTransaction, CommitIntentFunc, error) {
	if !s.IsProcessablePayment(p) {
		return nil, nil, ErrIntentNotAllowed
	}
	meth, err := payment_method.PaymentMethodByIDDB(s.ctx.PaymentDB(service.ReadOnly), p.Config.PaymentMethodID.Int64)
	if err != nil {
		return nil, nil, err
	}
	if !meth.Active() {
		return nil, nil, ErrPaymentMethodInactive
	}
	paymentTx := p.NewTransaction(payment.PaymentStatusOpen)
	paymentTx.Amount = paymentTx.Amount * -1
	return s.handleIntent(p, paymentTx, timeout)
}

func (s *Service) IntentCancel(p *payment.Payment, timeout time.Duration) (*payment.PaymentTransaction, CommitIntentFunc, error) {
	if p.Status != payment.PaymentStatusOpen {
		return nil, nil, ErrIntentNotAllowed
	}
	meth, err := payment_method.PaymentMethodByIDDB(s.ctx.PaymentDB(service.ReadOnly), p.Config.PaymentMethodID.Int64)
	if err != nil {
		return nil, nil, err
	}
	if meth.Disabled() {
		return nil, nil, ErrPaymentMethodDisabled
	}
	paymentTx := p.NewTransaction(payment.PaymentStatusCancelled)
	paymentTx.Amount = 0
	return s.handleIntent(p, paymentTx, timeout)
}

func (s *Service) IntentPaid(p *payment.Payment, timeout time.Duration) (*payment.PaymentTransaction, CommitIntentFunc, error) {
	if p.Status != payment.PaymentStatusOpen {
		return nil, nil, ErrIntentNotAllowed
	}
	meth, err := payment_method.PaymentMethodByIDDB(s.ctx.PaymentDB(service.ReadOnly), p.Config.PaymentMethodID.Int64)
	if err != nil {
		return nil, nil, err
	}
	if meth.Disabled() {
		return nil, nil, ErrPaymentMethodDisabled
	}
	paymentTx := p.NewTransaction(payment.PaymentStatusPaid)
	return s.handleIntent(p, paymentTx, timeout)
}

func (s *Service) IntentAuthorized(p *payment.Payment, timeout time.Duration) (*payment.PaymentTransaction, CommitIntentFunc, error) {
	if p.Status != payment.PaymentStatusOpen {
		return nil, nil, ErrIntentNotAllowed
	}
	meth, err := payment_method.PaymentMethodByIDDB(s.ctx.PaymentDB(service.ReadOnly), p.Config.PaymentMethodID.Int64)
	if err != nil {
		return nil, nil, err
	}
	if meth.Disabled() {
		return nil, nil, ErrPaymentMethodDisabled
	}
	paymentTx := p.NewTransaction(payment.PaymentStatusAuthorized)
	paymentTx.Amount = 0
	return s.handleIntent(p, paymentTx, timeout)
}

// CreatePaymentToken creates a new random payment token
func (s *Service) CreatePaymentToken(tx *sql.Tx, p *payment.Payment) (*payment.PaymentToken, error) {
	log := s.log.New(log15.Ctx{"method": "CreatePaymentToken"})
	token, err := payment.NewPaymentToken(p.PaymentID())
	if err != nil {
		log.Error("error creating payment token", log15.Ctx{"err": err})
		return nil, ErrInternal
	}
	err = payment.InsertPaymentTokenTx(tx, token)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			if mysqlErr.Number == 1213 {
				return nil, ErrDBLockTimeout
			}
		}
		log.Error("error saving payment token", log15.Ctx{"err": err})
		return nil, ErrDB
	}
	return token, nil
}

// PaymentByToken returns the payment associated with the given payment token
//
// TODO use token max age from config
func (s *Service) PaymentByToken(tx *sql.Tx, token string) (*payment.Payment, error) {
	tokenMaxAge := PaymentTokenMaxAgeDefault
	return payment.PaymentByTokenTx(tx, token, tokenMaxAge)
}

// DeletePaymentToken deletes/invalidates the given payment token
func (s *Service) DeletePaymentToken(tx *sql.Tx, token string) error {
	log := s.log.New(log15.Ctx{"method": "DeletePaymentToken"})
	err := payment.DeletePaymentTokenTx(tx, token)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			if mysqlErr.Number == 1213 {
				return ErrDBLockTimeout
			}
		}
		log.Error("error deleting payment token", log15.Ctx{"err": err})
		return ErrDB
	}
	return nil
}

type intentNotify struct {
	s *Service
}

func (n *intentNotify) CommitIntent(paymentTx *payment.PaymentTransaction) error {
	return n.s.notify(paymentTx)
}
