package stripe_test

import (
	"database/sql"
	"github.com/fritzpay/paymentd/pkg/paymentd/payment_method"
	"testing"
	"time"

	"github.com/fritzpay/paymentd/pkg/service/provider/stripe"

	"github.com/fritzpay/paymentd/pkg/testutil"
	. "github.com/smartystreets/goconvey/convey"
)

func TestPaypalTransaction(t *testing.T) {

	Convey("Given a payment DB", t, testutil.WithPaymentDB(t, func(db *sql.DB) {
		Convey("Given a DB tx", func() {
			tx, err := db.Begin()
			So(err, ShouldBeNil)

			method, err := payment_method.PaymentMethodByIDTx(tx, 1)
			So(err, ShouldBeNil)
			Convey("Inserting the Config", func() {
				c := &stripe.Config{
					ProjectID: method.ProjectID,
					MethodKey: method.MethodKey,
					Created:   time.Now(),
					CreatedBy: "gotest",
					SecretKey: "sk_test_ThisIs4Testing",
					PublicKey: "",
				}

				err = stripe.InsertConfigTx(tx, c)
				So(err, ShouldBeNil)
				stripe.ConfigByPaymentMethodTx(tx, method)
				Convey("It should succeed", func() {

				})

			})

		})
	}))
}
