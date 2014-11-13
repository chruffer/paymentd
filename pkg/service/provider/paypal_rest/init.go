package paypal_rest

import (
	"database/sql"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gopkg.in/inconshreveable/log15.v2"

	"github.com/fritzpay/paymentd/pkg/paymentd/payment"
	"github.com/fritzpay/paymentd/pkg/paymentd/payment_method"
)

const (
	paypalPaymentPath = "/v1/payments/payment"
)

func (d *Driver) InitPayment(p *payment.Payment, method *payment_method.Method) (http.Handler, error) {
	log := d.log.New(log15.Ctx{
		"method":          "InitPayment",
		"projectID":       p.ProjectID(),
		"paymentID":       p.ID(),
		"paymentMethodID": method.ID,
	})

	var tx *sql.Tx
	var err error
	var commit bool
	defer func() {
		if tx != nil && !commit {
			err = tx.Rollback()
			if err != nil {
				log.Crit("error on rollback", log15.Ctx{"err": err})
			}
		}
	}()
	tx, err = d.ctx.PaymentDB().Begin()
	if err != nil {
		commit = true
		log.Crit("error on begin tx", log15.Ctx{"err": err})
		return nil, ErrDatabase
	}

	currentTx, err := TransactionCurrentByPaymentIDTx(tx, p.PaymentID())
	if err != nil && err != ErrTransactionNotFound {
		log.Error("error retrieving transaction", log15.Ctx{"err": err})
		return nil, ErrDatabase
	}
	if err == nil {
		if Debug {
			log.Debug("already initialized payment")
		}
		return d.StatusHandler(currentTx, p), nil
	}

	cfg, err := ConfigByPaymentMethodTx(tx, method)
	if err != nil {
		log.Error("error retrieving PayPal config", log15.Ctx{"err": err})
		return nil, ErrDatabase
	}

	// create payment request
	req := &PayPalPaymentRequest{}
	if cfg.Type != "sale" && cfg.Type != "authorize" {
		log.Crit("invalid config type", log15.Ctx{"configType": cfg.Type})
		return nil, ErrInternal
	}
	req.Intent = cfg.Type
	req.Payer.PaymentMethod = PayPalPaymentMethodPayPal
	req.RedirectURLs, err = d.redirectURLs(p)
	if err != nil {
		log.Error("error creating redirect urls", log15.Ctx{"err": err})
		return nil, ErrInternal
	}
	req.Transactions = []PayPalTransaction{
		d.payPalTransactionFromPayment(p),
	}
	if Debug {
		log.Debug("created paypal payment request", log15.Ctx{"request": req})
	}

	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		log.Error("error on endpoint URL", log15.Ctx{"err": err})
		return nil, ErrInternal
	}
	endpoint.Path = paypalPaymentPath

	jsonBytes, err := json.Marshal(req)
	if err != nil {
		log.Error("error encoding request", log15.Ctx{"err": err})
		return nil, ErrInternal
	}

	paypalTx := &Transaction{
		ProjectID: p.ProjectID(),
		PaymentID: p.ID(),
		Timestamp: time.Now(),
		Type:      TransactionTypeCreatePayment,
	}
	paypalTx.SetIntent(cfg.Type)
	paypalTx.Data = jsonBytes

	err = InsertTransactionTx(tx, paypalTx)
	if err != nil {
		log.Error("error saving transaction", log15.Ctx{"err": err})
		return nil, ErrDatabase
	}

	commit = true
	err = tx.Commit()
	if err != nil {
		log.Crit("error on commit", log15.Ctx{"err": err})
		return nil, ErrDatabase
	}

	errors := make(chan error)
	go func() {
		for {
			select {
			case err := <-errors:
				if err == nil {
					return
				}
				log.Error("error on initializing", log15.Ctx{"err": err})
				return
			case <-d.ctx.Done():
				log.Warn("cancelled initialization", log15.Ctx{"err": d.ctx.Err()})
				return
			}
		}
	}()
	go d.doInit(errors, cfg, endpoint, p, string(jsonBytes))

	return d.InitPageHandler(p), nil
}

func (d *Driver) redirectURLs(p *payment.Payment) (PayPalRedirectURLs, error) {
	u := PayPalRedirectURLs{}
	returnRoute, err := d.mux.Get("returnHandler").URLPath()
	if err != nil {
		return u, err
	}
	cancelRoute, err := d.mux.Get("cancelHandler").URLPath()
	if err != nil {
		return u, err
	}

	q := url.Values(make(map[string][]string))
	q.Set("paymentID", d.paymentService.EncodedPaymentID(p.PaymentID()).String())

	returnURL := &(*d.baseURL)
	returnURL.Path = returnRoute.Path
	returnURL.RawQuery = q.Encode()
	u.ReturnURL = returnURL.String()

	cancelURL := &(*d.baseURL)
	cancelURL.Path = cancelRoute.Path
	cancelURL.RawQuery = q.Encode()
	u.CancelURL = cancelURL.String()

	return u, nil
}

func (d *Driver) payPalTransactionFromPayment(p *payment.Payment) PayPalTransaction {
	t := PayPalTransaction{}
	encPaymentID := d.paymentService.EncodedPaymentID(p.PaymentID())
	t.Custom = encPaymentID.String()
	t.InvoiceNumber = encPaymentID.String()
	t.Amount = PayPalAmount{
		Currency: p.Currency,
		Total:    p.DecimalRound(2).String(),
	}
	return t
}

func (d *Driver) doInit(errors chan<- error, cfg *Config, reqURL *url.URL, p *payment.Payment, body string) {
	log := d.log.New(log15.Ctx{
		"method":      "doInit",
		"projectID":   p.ProjectID(),
		"paymentID":   p.ID(),
		"methodKey":   cfg.MethodKey,
		"requestBody": body,
	})
	if Debug {
		log.Debug("posting...")
	}

	tr, err := d.oAuthTransport(log)(p, cfg)
	if err != nil {
		log.Error("error on auth transport", log15.Ctx{"err": err})
		errors <- err
		return
	}
	err = tr.AuthenticateClient()
	if err != nil {
		log.Error("error authenticating", log15.Ctx{"err": err})
		errors <- err
		return
	}
	cl := tr.Client()
	resp, err := cl.Post(reqURL.String(), "application/json", strings.NewReader(body))
	if err != nil {
		log.Error("error on HTTP POST", log15.Ctx{"err": err})
		errors <- err
		return
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		log.Error("error on HTTP request", log15.Ctx{"HTTPStatusCode": resp.StatusCode})
		d.setPayPalErrorResponse(p, nil)
		errors <- ErrHTTP
		return
	}
	respBody, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		log.Error("error reading response body", log15.Ctx{"err": err})
		d.setPayPalErrorResponse(p, nil)
		errors <- ErrHTTP
		return
	}
	log = log.New(log15.Ctx{"responseBody": string(respBody)})
	if Debug {
		log.Debug("received response")
	}
	paypalP := &PaypalPayment{}
	err = json.Unmarshal(respBody, paypalP)
	if err != nil {
		log.Error("error decoding PayPal response", log15.Ctx{"err": err})
		d.setPayPalErrorResponse(p, respBody)
		errors <- ErrProvider
	}

	paypalTx := &Transaction{
		ProjectID: p.ProjectID(),
		PaymentID: p.ID(),
		Timestamp: time.Now(),
		Type:      TransactionTypeCreatePaymentResponse,
	}
	if paypalP.Intent != "" {
		paypalTx.SetIntent(paypalP.Intent)
	}
	if paypalP.ID != "" {
		paypalTx.SetPaypalID(paypalP.ID)
	}
	if paypalP.State != "" {
		paypalTx.SetState(paypalP.State)
	}
	if paypalP.CreateTime != "" {
		t, err := time.Parse(time.RFC3339, paypalP.CreateTime)
		if err != nil {
			log.Warn("error parsing paypal create time", log15.Ctx{"err": err})
		} else {
			paypalTx.PaypalCreateTime = &t
		}
	}
	if paypalP.UpdateTime != "" {
		t, err := time.Parse(time.RFC3339, paypalP.UpdateTime)
		if err != nil {
			log.Warn("error parsing paypal update time", log15.Ctx{"err": err})
		} else {
			paypalTx.PaypalUpdateTime = &t
		}
	}
	paypalTx.Links, err = json.Marshal(paypalP.Links)
	if err != nil {
		log.Error("error on saving links on response", log15.Ctx{"err": err})
		d.setPayPalErrorResponse(p, respBody)
		errors <- ErrProvider
		return
	}
	paypalTx.Data, err = json.Marshal(paypalP)
	if err != nil {
		log.Error("error marshalling paypal payment response", log15.Ctx{"err": err})
		d.setPayPalErrorResponse(p, respBody)
		errors <- ErrProvider
		return
	}
	err = InsertTransactionDB(d.ctx.PaymentDB(), paypalTx)
	if err != nil {
		log.Error("error saving paypal response", log15.Ctx{"err": err})
		d.setPayPalErrorResponse(p, respBody)
		errors <- ErrProvider
		return
	}

	close(errors)
}