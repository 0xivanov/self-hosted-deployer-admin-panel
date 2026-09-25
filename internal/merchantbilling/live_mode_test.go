//go:build integration

package merchantbilling

import (
	"testing"

	stripe "github.com/stripe/stripe-go/v86"
)

func TestLiveClientModeAndKeyValidation(t *testing.T) {
	if _, err := NewLiveClient("sk_test_synthetic_fixture", "https://portal.example.test/merchant/return", "https://portal.example.test/merchant/refresh", []string{"BG"}); err == nil {
		t.Fatal("accepted test key for live client")
	}
	live, err := NewLiveClient("sk_live_synthetic_fixture", "https://portal.example.test/merchant/return", "https://portal.example.test/merchant/refresh", []string{"BG"})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if live.BillingMode() != "live" {
		t.Fatal("live client lost mode")
	}
	test, err := NewTestClient("sk_test_synthetic_fixture", "https://portal.example.test/merchant/return", "https://portal.example.test/merchant/refresh", []string{"BG"})
	if err != nil {
		t.Fatal(err)
	}
	defer test.Close()
	if test.BillingMode() != "test" {
		t.Fatal("test client lost mode")
	}
}

func TestCheckoutNormalizerRequiresSelectedMode(t *testing.T) {
	order := CheckoutOrder{RequestID: requestFixture, Name: "Example", Currency: "eur", AmountMinor: 1250}
	session := &stripe.CheckoutSession{
		Object: "checkout.session", ID: "cs_live_fixture", Livemode: true, Mode: stripe.CheckoutSessionModePayment,
		ClientReferenceID: order.RequestID, Metadata: map[string]string{"merchant_order": order.RequestID},
		Currency: stripe.CurrencyEUR, AmountSubtotal: order.AmountMinor, AmountTotal: order.AmountMinor,
		SuccessURL: "https://portal.example.test/merchant/sales/success", CancelURL: "https://portal.example.test/merchant/sales/cancel",
		PaymentMethodTypes: []string{"card"}, Status: stripe.CheckoutSessionStatusComplete,
		PaymentStatus: stripe.CheckoutSessionPaymentStatusUnpaid,
	}
	c := &Client{live: true, returnURL: "https://portal.example.test/merchant/return"}
	if _, err := c.normalizeCheckout(session, order, "cs_live_fixture"); err != nil {
		t.Fatal(err)
	}
	session.PaymentStatus = stripe.CheckoutSessionPaymentStatusPaid
	session.PaymentIntent = &stripe.PaymentIntent{ID: "pi_live_fixture", Object: "payment_intent", Livemode: true}
	if _, err := c.normalizeCheckout(session, order, session.ID); err != nil {
		t.Fatal(err)
	}
	session.PaymentIntent.Livemode = false
	if _, err := c.normalizeCheckout(session, order, session.ID); err == nil {
		t.Fatal("accepted opposite-mode expanded checkout payment intent")
	}
	session.PaymentIntent = nil
	session.Livemode = false
	if _, err := c.normalizeCheckout(session, order, session.ID); err == nil {
		t.Fatal("accepted opposite-mode checkout")
	}
	if validSessionIDForMode("cs_test_fixture", true) || !validSessionIDForMode("cs_live_fixture", true) || !validSessionID("cs_test_fixture") {
		t.Fatal("session mode validation failed")
	}
}

func TestRefundNormalizerRequiresExpandedPaymentIntentMode(t *testing.T) {
	request := refundRequestFixture()
	c := &Client{live: true}
	refund := &stripe.Refund{
		Object: "refund", ID: "re_live_fixture", Amount: request.AmountMinor, Currency: stripe.Currency(request.Currency),
		PaymentIntent: &stripe.PaymentIntent{Object: "payment_intent", ID: request.PaymentIntentID, Livemode: true},
		Metadata:      map[string]string{"merchant_refund": request.RequestID, "merchant_order": request.OrderID}, Status: stripe.RefundStatusSucceeded,
	}
	if _, err := c.normalizeRefund(refund, request, refund.ID); err != nil {
		t.Fatal(err)
	}
	refund.PaymentIntent.Livemode = false
	if _, err := c.normalizeRefund(refund, request, refund.ID); err == nil {
		t.Fatal("accepted mismatched payment intent mode")
	}
}

func TestTestRefundRejectsLiveExpandedPaymentIntent(t *testing.T) {
	request := refundRequestFixture()
	refund := &stripe.Refund{Object: "refund", ID: "re_mode", Amount: request.AmountMinor, Currency: stripe.Currency(request.Currency), PaymentIntent: &stripe.PaymentIntent{ID: request.PaymentIntentID, Object: "payment_intent", Livemode: true}, Metadata: map[string]string{"merchant_refund": request.RequestID, "merchant_order": request.OrderID}, Status: stripe.RefundStatusSucceeded}
	if _, err := (&Client{}).normalizeRefund(refund, request, refund.ID); err == nil {
		t.Fatal("test refund accepted live payment intent")
	}
}
