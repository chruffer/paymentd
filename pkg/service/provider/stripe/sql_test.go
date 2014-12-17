package stripe_test

import (
	"database/sql"
	"github.com/fritzpay/paymentd/pkg/paymentd/payment"
	test_payment "github.com/fritzpay/paymentd/pkg/testutil/payment"

	"github.com/fritzpay/paymentd/pkg/paymentd/payment_method"
	"testing"
	"time"

	"github.com/fritzpay/paymentd/pkg/service/provider/stripe"

	"github.com/fritzpay/paymentd/pkg/testutil"
	. "github.com/smartystreets/goconvey/convey"
)

func TestStripeConfig(t *testing.T) {

	Convey("Given a payment DB", t, testutil.WithPaymentDB(t, func(db *sql.DB) {
		Convey("Given a DB tx", func() {
			tx, err := db.Begin()
			So(err, ShouldBeNil)

			testProjectID := int64(1)
			testKey := "sk_test_ThisIs4Testing"

			method, err := payment_method.PaymentMethodByIDTx(tx, testProjectID)
			So(err, ShouldBeNil)
			Convey("Inserting the Config", func() {
				c := &stripe.Config{
					ProjectID: method.ProjectID,
					MethodKey: method.MethodKey,
					Created:   time.Now(),
					CreatedBy: "gotest",
					SecretKey: testKey,
					PublicKey: "",
				}

				err = stripe.InsertConfigTx(tx, c)
				Convey("It should succeed", func() {
					So(err, ShouldBeNil)
				})
				err = tx.Commit()
				Convey("Commit should succeed", func() {
					So(err, ShouldBeNil)
				})
			})

			Convey("Retrieving the Config", func() {

				c, err := stripe.ConfigByPaymentMethodTx(tx, method)
				tc := &stripe.Config{}
				Convey("It should succeed", func() {
					So(err, ShouldBeNil)
					So(c, ShouldHaveSameTypeAs, tc)
				})
				Convey("It should have the correct config values", func() {
					So(c.ProjectID, ShouldEqual, testProjectID)
					So(c.SecretKey, ShouldEqual, testKey)
					So(c.MethodKey, ShouldEqual, method.MethodKey)
				})
			})

		})
	}))
}

func TestStripeTransaction(t *testing.T) {

	Convey("Given a payment DB", t, testutil.WithPaymentDB(t, func(db *sql.DB) {
		Convey("Given a DB tx", func() {
			tx, err := db.Begin()
			So(err, ShouldBeNil)

			testStripeChargeID := "test"
			testStripeTxID := "test"
			testStripeCardToken := "test"
			testTimeStamp := time.Now()
			var testPayment *payment.Payment
			var stx *stripe.Transaction

			test_payment.WithPaymentInTx(tx, func(p *payment.Payment) {
				testPayment = p
				stx = &stripe.Transaction{
					ProjectID:  p.ProjectID(),
					PaymentID:  p.ID(),
					Timestamp:  testTimeStamp,
					CreateTime: int64(testTimeStamp.Second()),
					ChargeID:   testStripeChargeID,
					TxID:       testStripeTxID,
					Paid:       true,
					CardToken:  testStripeCardToken,
				}

				Convey("Inserting a Transaction", func() {
					err = stripe.InsertTransactionTx(tx, stx)
					Convey("It should succeed", func() {
						So(err, ShouldBeNil)
					})
					err = tx.Commit()
					Convey("Commit should succeed", func() {
						So(err, ShouldBeNil)
					})
				})

				Convey("Retrieving the Transaction", func() {

					stx, err := stripe.TransactionByPaymentIDTx(tx, testPayment.PaymentID())
					Convey("It should succeed", func() {
						So(err, ShouldBeNil)
					})
					Convey("It should have the correct transaction values", func() {
						So(stx.ProjectID, ShouldEqual, testPayment.PaymentID().ProjectID)
						So(stx.PaymentID, ShouldEqual, testPayment.PaymentID().PaymentID)
						So(stx.ChargeID, ShouldEqual, testStripeChargeID)
						So(stx.TxID, ShouldEqual, testStripeTxID)
					})
				})
			})
		})
	}))
}
