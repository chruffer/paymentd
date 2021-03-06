package payment_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fritzpay/paymentd/pkg/service/payment/notification/v2"

	testPay "github.com/fritzpay/paymentd/pkg/testutil/payment"

	"github.com/fritzpay/paymentd/pkg/paymentd/payment"
	"github.com/fritzpay/paymentd/pkg/paymentd/project"
	"github.com/fritzpay/paymentd/pkg/service"
	paymentService "github.com/fritzpay/paymentd/pkg/service/payment"
	"github.com/fritzpay/paymentd/pkg/testutil"
	. "github.com/smartystreets/goconvey/convey"
	"gopkg.in/inconshreveable/log15.v2"
)

func WithService(ctx *service.Context, f func(s *paymentService.Service)) func() {
	return func() {
		s, err := paymentService.NewService(ctx)
		So(err, ShouldBeNil)

		f(s)
	}
}

func TestPaymentNotification(t *testing.T) {
	Convey("Given a payment db connection", t, testutil.WithPaymentDB(t, func(db *sql.DB) {
		Convey("Given a principal db connection", testutil.WithPrincipalDB(t, func(principalDB *sql.DB) {
			Convey("Given a transaction", func() {
				tx, err := db.Begin()
				So(err, ShouldBeNil)
				Reset(func() {
					tx.Rollback()
				})

				Convey("Given a service context", testutil.WithContext(func(ctx *service.Context, logs <-chan *log15.Record) {
					ctx.SetPaymentDB(db, nil)
					ctx.SetPrincipalDB(principalDB, nil)

					Convey("Given a payment service", WithService(ctx, func(s *paymentService.Service) {

						Convey("Given a payment", testPay.WithPaymentInTx(tx, func(p *payment.Payment) {

							Convey("Given a test HTTP server", func(c C) {
								srvOk := make(chan struct{})
								var req *http.Request
								var body []byte
								testSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
									req = r
									body, err = ioutil.ReadAll(r.Body)
									c.So(err, ShouldBeNil)
									close(srvOk)
								}))

								Reset(func() {
									testSrv.Close()
								})

								Convey("Given a test callback configuration", func() {
									testPk, err := project.ProjectKeyByKeyDB(principalDB, "testkey")
									So(err, ShouldBeNil)
									So(testPk.IsValid(), ShouldBeTrue)

									p.Config.SetCallbackURL(testSrv.URL)
									p.Config.SetCallbackAPIVersion("2")
									p.Config.SetCallbackProjectKey(testPk.Key)

									So(paymentService.CanCallback(&p.Config), ShouldBeTrue)

									Convey("When the payment has no transaction", func() {
										paymentTx, err := s.PaymentTransaction(tx, p)
										So(err, ShouldEqual, payment.ErrPaymentTransactionNotFound)
										So(paymentTx.Status.Valid(), ShouldBeFalse)

										Convey("When creating a transaction", func() {
											So(s.IsProcessablePayment(p), ShouldBeTrue)
											var commitIntent paymentService.CommitIntentFunc
											paymentTx, commitIntent, err = s.IntentOpen(p, 500*time.Millisecond)
											So(err, ShouldBeNil)
											So(commitIntent, ShouldNotBeNil)
											So(paymentTx.Timestamp.UnixNano(), ShouldNotEqual, 0)
											err = s.SetPaymentTransaction(tx, paymentTx)
											So(err, ShouldBeNil)

											Convey("When committing the transaction", func() {
												err = tx.Commit()
												So(err, ShouldBeNil)

												Convey("When requesting a notification", func() {
													commitIntent()

													Convey("A notification should be sent", func() {
														select {
														case <-srvOk:
															So(req, ShouldNotBeNil)
														case <-time.After(time.Second):
															t.Errorf("request timeout on %s", testSrv.URL)
															close(srvOk)
														drain:
															for {
																select {
																case msg := <-logs:
																	t.Logf("%v", msg)
																default:
																	break drain
																}
															}
														}

														Convey("The notification should contain the transaction", func() {
															not := &notification.Notification{}
															dec := json.NewDecoder(bytes.NewBuffer(body))
															err := dec.Decode(not)
															So(err, ShouldBeNil)

															So(not.TransactionTimestamp, ShouldNotEqual, 0)
															So(not.Status, ShouldEqual, payment.PaymentStatusOpen)
														})
													})
												})
											})
										})
									})
								})
							})
						}))
					}))
				}))
			})
		}))
	}))
}
