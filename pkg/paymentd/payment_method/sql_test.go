package payment_method

import (
	"database/sql"
	"testing"

	"github.com/fritzpay/paymentd/pkg/paymentd/principal"
	"github.com/fritzpay/paymentd/pkg/paymentd/project"
	"github.com/fritzpay/paymentd/pkg/paymentd/provider"
	"github.com/fritzpay/paymentd/pkg/testutil"
	. "github.com/smartystreets/goconvey/convey"
)

func TestPaymentMethodSQL(t *testing.T) {
	Convey("Given a payment DB connection", t, testutil.WithPaymentDB(t, func(db *sql.DB) {
		Reset(func() {
			db.Close()
		})
		Convey("Given a principal DB connection", testutil.WithPrincipalDB(t, func(prDB *sql.DB) {
			Reset(func() {
				prDB.Close()
			})
			Convey("Given a test principal", func() {
				princ, err := principal.PrincipalByNameDB(prDB, "testprincipal")
				So(err, ShouldBeNil)
				So(princ.ID, ShouldNotEqual, 0)
				So(princ.Empty(), ShouldBeFalse)

				Convey("Given a test project", func() {
					proj, err := project.ProjectByPrincipalIDNameDB(prDB, princ.ID, "testproject")
					So(err, ShouldBeNil)

					Convey("Given a transaction", func() {
						tx, err := db.Begin()
						So(err, ShouldBeNil)

						Reset(func() {
							err = tx.Rollback()
							So(err, ShouldBeNil)
						})

						Convey("Given a test provider exists", func() {
							pr, err := provider.ProviderByNameTx(tx, "fritzpay")
							So(err, ShouldBeNil)
							So(pr.Name, ShouldEqual, "fritzpay")

							Convey("When retrieving a nonexistent payment method", func() {
								_, err = PaymentMethodByProjectIDProviderNameMethodKeyTx(tx, proj.ID, pr.Name, "nonexistent")
								Convey("It should return a not found error", func() {
									So(err, ShouldEqual, ErrPaymentMethodNotFound)
								})
							})

							Convey("When retrieving an existent payment method", func() {
								pm, err := PaymentMethodByProjectIDProviderNameMethodKeyTx(tx, proj.ID, pr.Name, "test")
								Convey("It should return a payment method", func() {
									So(err, ShouldBeNil)
									So(pm.MethodKey, ShouldEqual, "test")
								})
							})

							Convey("When inserting a new payment method", func() {
								pm := &Method{}
								pm.ProjectID = proj.ID
								pm.Provider.Name = pr.Name
								pm.MethodKey = "testInsert"
								pm.CreatedBy = "test"

								err = InsertPaymentMethodTx(tx, pm)
								So(err, ShouldBeNil)
								So(pm.ID, ShouldNotEqual, 0)

								Convey("When setting the status to active", func() {
									pm.Status = PaymentMethodStatusActive
									pm.CreatedBy = "test"

									err = InsertPaymentMethodStatusTx(tx, pm)
									So(err, ShouldBeNil)

									Convey("When retrieving the payment method", func() {
										newPm, err := PaymentMethodByIDTx(tx, pm.ID)
										So(err, ShouldBeNil)

										Convey("The retrieved payment method should match", func() {
											So(newPm.Status, ShouldEqual, PaymentMethodStatusActive)
										})
									})
								})

								Convey("When setting metadata", func() {
									pm.Metadata = map[string]string{
										"name": "value",
										"test": "check",
									}
									err = InsertPaymentMethodMetadataTx(tx, pm, "metatest")
									So(err, ShouldBeNil)

									Convey("When selecting metadata", func() {
										metadata, err := PaymentMethodMetadataTx(tx, pm)
										So(err, ShouldBeNil)

										Convey("It should match", func() {
											So(metadata, ShouldNotBeNil)
											So(metadata["name"], ShouldEqual, "value")
											So(metadata["test"], ShouldEqual, "check")
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
}
