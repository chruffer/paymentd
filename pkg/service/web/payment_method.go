package web

import (
	"net/http"

	"github.com/fritzpay/paymentd/pkg/paymentd/payment"
)

func (h *Handler) SelectPaymentMethodHandler(p *payment.Payment) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

	})
}
