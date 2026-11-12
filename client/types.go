package client

import (
	"encoding/json"
	"time"
)

// Amount is a monetary amount in the smallest currency unit (cents for EUR).
type Amount struct {
	Value    int64  `json:"value"`
	Currency string `json:"currency,omitempty"`
}

// EUR is a helper to create an amount in euro cents.
func EUR(cents int64) Amount { return Amount{Value: cents, Currency: "EUR"} }

// PaymentMethod selects the payment method directly (skipping the method
// selection on the hosted checkout page).
type PaymentMethod struct {
	ID    int            `json:"id"`
	Input map[string]any `json:"input,omitempty"`
}

// Address is a delivery or invoice address.
type Address struct {
	FirstName            string `json:"firstName,omitempty"`
	LastName             string `json:"lastName,omitempty"`
	Street               string `json:"street,omitempty"`
	StreetNumber         string `json:"streetNumber,omitempty"`
	StreetNumberAddition string `json:"streetNumberAddition,omitempty"`
	ZipCode              string `json:"zipCode,omitempty"`
	City                 string `json:"city,omitempty"`
	Region               string `json:"region,omitempty"`
	Country              string `json:"country,omitempty"` // ISO 3166-1 alpha-2
}

// Company describes the company of a customer.
type Company struct {
	Name      string `json:"name,omitempty"`
	CocNumber string `json:"cocNumber,omitempty"`
	VatNumber string `json:"vatNumber,omitempty"`
	Country   string `json:"country,omitempty"`
}

// Customer describes the paying customer.
type Customer struct {
	Email     string   `json:"email,omitempty"`
	FirstName string   `json:"firstname,omitempty"`
	LastName  string   `json:"lastname,omitempty"`
	BirthDate string   `json:"birthDate,omitempty"` // YYYY-MM-DD
	Gender    string   `json:"gender,omitempty"`
	Phone     string   `json:"phone,omitempty"`
	Locale    string   `json:"locale,omitempty"`
	IPAddress string   `json:"ipAddress,omitempty"`
	Reference string   `json:"reference,omitempty"`
	Company   *Company `json:"company,omitempty"`
}

// Product types as understood by Pay.nl.
const (
	ProductTypeArticle  = "ARTICLE"
	ProductTypeShipping = "SHIPPING"
	ProductTypeHandling = "HANDLING"
	ProductTypeDiscount = "DISCOUNT"
	ProductTypeGiftCard = "GIFTCARD"
)

// Product is a single order line.
type Product struct {
	ID            string  `json:"id,omitempty"`
	Description   string  `json:"description,omitempty"`
	Type          string  `json:"type,omitempty"`
	Price         Amount  `json:"price"`
	Quantity      int     `json:"quantity"`
	VatPercentage float64 `json:"vatPercentage,omitempty"`
}

// OrderDetails holds order lines and addresses (used for BNPL methods and reporting).
type OrderDetails struct {
	CountryCode     string    `json:"countryCode,omitempty"`
	DeliveryDate    string    `json:"deliveryDate,omitempty"`
	InvoiceDate     string    `json:"invoiceDate,omitempty"`
	DeliveryAddress *Address  `json:"deliveryAddress,omitempty"`
	InvoiceAddress  *Address  `json:"invoiceAddress,omitempty"`
	Products        []Product `json:"products,omitempty"`
}

// Stats are free-form fields stored with the order, returned in exchange calls.
type Stats struct {
	Info   string `json:"info,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Object string `json:"object,omitempty"`
	Extra1 string `json:"extra1,omitempty"`
	Extra2 string `json:"extra2,omitempty"`
	Extra3 string `json:"extra3,omitempty"`
	Domain string `json:"domainId,omitempty"`
}

// Integration controls integration specific behaviour.
type Integration struct {
	Test bool `json:"test"`
}

// CreateOrderRequest is the request body for creating an order.
type CreateOrderRequest struct {
	ServiceID     string         `json:"serviceId,omitempty"`
	Description   string         `json:"description,omitempty"`
	Reference     string         `json:"reference,omitempty"`
	Expire        *time.Time     `json:"expire,omitempty"`
	ReturnURL     string         `json:"returnUrl,omitempty"`
	ExchangeURL   string         `json:"exchangeUrl,omitempty"`
	Amount        Amount         `json:"amount"`
	PaymentMethod *PaymentMethod `json:"paymentMethod,omitempty"`
	Customer      *Customer      `json:"customer,omitempty"`
	Order         *OrderDetails  `json:"order,omitempty"`
	Stats         *Stats         `json:"stats,omitempty"`
	Integration   *Integration   `json:"integration,omitempty"`
	TransferData  map[string]any `json:"transferData,omitempty"`
}

// Links are the HATEOAS links returned with an order.
type Links struct {
	Status          string `json:"status,omitempty"`
	Abort           string `json:"abort,omitempty"`
	Approve         string `json:"approve,omitempty"`
	Decline         string `json:"decline,omitempty"`
	Void            string `json:"void,omitempty"`
	Capture         string `json:"capture,omitempty"`
	CaptureAmount   string `json:"captureAmount,omitempty"`
	CaptureProducts string `json:"captureProducts,omitempty"`
	Checkout        string `json:"checkout,omitempty"`
	Redirect        string `json:"redirect,omitempty"`
}

// OrderPayment is a payment attempt made for an order.
type OrderPayment struct {
	ID                        string         `json:"id,omitempty"`
	PaymentMethod             PaymentMethod  `json:"paymentMethod"`
	CustomerType              string         `json:"customerType,omitempty"`
	CustomerKey               string         `json:"customerKey,omitempty"`
	CustomerID                string         `json:"customerId,omitempty"`
	CustomerName              string         `json:"customerName,omitempty"`
	IPAddress                 string         `json:"ipAddress,omitempty"`
	SecureStatus              bool           `json:"secureStatus,omitempty"`
	PaymentVerificationMethod int            `json:"paymentVerificationMethod,omitempty"`
	Status                    Status         `json:"status"`
	CurrencyAmount            Amount         `json:"currencyAmount"`
	Amount                    Amount         `json:"amount"`
	AuthorizedAmount          Amount         `json:"authorizedAmount"`
	CapturedAmount            Amount         `json:"capturedAmount"`
	SupplierData              map[string]any `json:"supplierData,omitempty"`
}

// Order is an order as returned by the Order API.
type Order struct {
	ID                 string         `json:"id"`
	ServiceID          string         `json:"serviceId,omitempty"`
	Description        string         `json:"description,omitempty"`
	Reference          string         `json:"reference,omitempty"`
	ManualTransferCode string         `json:"manualTransferCode,omitempty"`
	OrderID            string         `json:"orderId,omitempty"`
	UUID               string         `json:"uuid,omitempty"`
	CustomerKey        string         `json:"customerKey,omitempty"`
	Status             Status         `json:"status"`
	ReceiptURL         string         `json:"receiptUrl,omitempty"`
	IntegrationTest    bool           `json:"integration,omitempty"`
	Amount             Amount         `json:"amount"`
	AuthorizedAmount   *Amount        `json:"authorizedAmount,omitempty"`
	CapturedAmount     *Amount        `json:"capturedAmount,omitempty"`
	CheckoutData       map[string]any `json:"checkoutData,omitempty"`
	Payments           []OrderPayment `json:"payments,omitempty"`
	CreatedAt          *time.Time     `json:"createdAt,omitempty"`
	CreatedBy          string         `json:"createdBy,omitempty"`
	ModifiedAt         *time.Time     `json:"modifiedAt,omitempty"`
	ModifiedBy         string         `json:"modifiedBy,omitempty"`
	ExpiresAt          *time.Time     `json:"expiresAt,omitempty"`
	CompletedAt        *time.Time     `json:"completedAt,omitempty"`
	Links              Links          `json:"links"`

	// Raw holds the raw JSON as returned by the API.
	Raw json.RawMessage `json:"-"`
}

func (o *Order) setRaw(b []byte) { o.Raw = append(json.RawMessage(nil), b...) }

// CheckoutURL returns the URL the customer should be redirected to.
func (o *Order) CheckoutURL() string {
	if o.Links.Checkout != "" {
		return o.Links.Checkout
	}
	return o.Links.Redirect
}

// RefundRequest is the request body for refunding a transaction.
type RefundRequest struct {
	// Amount in cents. Zero (or omitted) refunds the full remaining amount.
	Amount int64 `json:"amount,omitempty"`

	// Description is shown on the customer's bank statement where supported.
	Description string `json:"description,omitempty"`

	// ProcessDate optionally schedules the refund (YYYY-MM-DD).
	ProcessDate string `json:"processDate,omitempty"`

	// VatPercentage optionally sets the VAT percentage for the refund.
	VatPercentage float64 `json:"vatPercentage,omitempty"`

	// Products optionally refunds specific products (by product id -> quantity).
	Products map[string]int `json:"products,omitempty"`

	// ExchangeURL optionally overrides the exchange url for refund callbacks.
	ExchangeURL string `json:"exchangeUrl,omitempty"`
}

// RefundResponse is the response of a refund call.
//
// The REST API response format differs per refund type (direct / IBAN batch),
// only the common fields are decoded. Raw contains the full response body.
type RefundResponse struct {
	TransactionID  string          `json:"transactionId,omitempty"`
	RefundID       string          `json:"refundId,omitempty"`
	Description    string          `json:"description,omitempty"`
	ProcessDate    string          `json:"processDate,omitempty"`
	AmountRefunded json.RawMessage `json:"amountRefunded,omitempty"`
	RefundedAmount json.RawMessage `json:"refundedAmount,omitempty"`
	Raw            json.RawMessage `json:"-"`
}

func (r *RefundResponse) setRaw(b []byte) { r.Raw = append(json.RawMessage(nil), b...) }
