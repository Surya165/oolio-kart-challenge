package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kart/internal/catalog"
	"kart/internal/order"
	"kart/internal/promo"
)

func newTestServer(t *testing.T) (*httptest.Server, *order.MemoryRepository) {
	t.Helper()
	products, err := catalog.NewSeeded()
	if err != nil {
		t.Fatal(err)
	}
	repo := order.NewMemoryRepository()
	s := &Server{
		Catalog: products,
		Orders: &order.Service{
			Catalog: products,
			Promo:   promo.NewSetValidator([]string{"HAPPYHRS", "FIFTYOFF"}),
			Repo:    repo,
		},
		APIKey: "apitest",
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, repo
}

func TestListProducts(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/product")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var ps []catalog.Product
	if err := json.NewDecoder(resp.Body).Decode(&ps); err != nil {
		t.Fatal(err)
	}
	if len(ps) != 9 {
		t.Fatalf("got %d products, want 9", len(ps))
	}
}

func TestGetProduct(t *testing.T) {
	ts, _ := newTestServer(t)
	tests := []struct {
		name, id string
		want     int
	}{
		{"exists", "1", 200},
		{"leading zeros", "001", 200},
		{"missing", "999", 404},
		{"not a number", "abc", 400},
		{"zero", "0", 400},
		{"negative", "-1", 400},
		{"overflow int64", "99999999999999999999", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + "/api/product/" + tt.id)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("GET /api/product/%s = %d, want %d", tt.id, resp.StatusCode, tt.want)
			}
		})
	}
}

func TestPlaceOrder(t *testing.T) {
	ts, _ := newTestServer(t)
	tests := []struct {
		name    string
		apiKey  string
		body    string
		want    int
		wantMsg string
	}{
		{"ok without coupon", "apitest", `{"items":[{"productId":"1","quantity":2}]}`, 200, ""},
		{"ok with valid coupon", "apitest", `{"couponCode":"HAPPYHRS","items":[{"productId":"1","quantity":1}]}`, 200, ""},
		{"empty coupon treated as none", "apitest", `{"couponCode":"","items":[{"productId":"1","quantity":1}]}`, 200, ""},
		{"invalid coupon", "apitest", `{"couponCode":"SUPER100","items":[{"productId":"1","quantity":1}]}`, 422, "couponCode"},
		{"coupon wrong case", "apitest", `{"couponCode":"happyhrs","items":[{"productId":"1","quantity":1}]}`, 422, "couponCode"},
		{"coupon too short", "apitest", `{"couponCode":"HAPPY","items":[{"productId":"1","quantity":1}]}`, 422, "couponCode"},
		{"missing api key", "", `{"items":[{"productId":"1","quantity":1}]}`, 401, ""},
		{"wrong api key", "nope", `{"items":[{"productId":"1","quantity":1}]}`, 403, ""},
		{"malformed json", "apitest", `{"items":[`, 400, ""},
		{"empty body", "apitest", ``, 400, ""},
		{"trailing data", "apitest", `{"items":[{"productId":"1","quantity":1}]} {}`, 400, ""},
		{"wrong type", "apitest", `{"items":[{"productId":1,"quantity":1}]}`, 400, ""},
		{"no items", "apitest", `{"items":[]}`, 422, "items"},
		{"items missing", "apitest", `{}`, 422, "items"},
		{"missing productId", "apitest", `{"items":[{"quantity":1}]}`, 422, "productId"},
		{"missing quantity", "apitest", `{"items":[{"productId":"1"}]}`, 422, "quantity"},
		{"zero quantity", "apitest", `{"items":[{"productId":"1","quantity":0}]}`, 422, "quantity"},
		{"negative quantity", "apitest", `{"items":[{"productId":"1","quantity":-3}]}`, 422, "quantity"},
		{"unknown product", "apitest", `{"items":[{"productId":"999","quantity":1}]}`, 422, "productId"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/order", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			if tt.apiKey != "" {
				req.Header.Set("api_key", tt.apiKey)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d; body %s", resp.StatusCode, tt.want, b)
			}
			if tt.want != 200 {
				var e APIResponse
				if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
					t.Fatalf("error body not ApiResponse: %v", err)
				}
				if e.Code != tt.want || !strings.Contains(e.Message, tt.wantMsg) {
					t.Fatalf("error = %+v, want code %d mentioning %q", e, tt.want, tt.wantMsg)
				}
			}
		})
	}
}

func TestPlaceOrderResponseShape(t *testing.T) {
	ts, repo := newTestServer(t)
	body := `{"couponCode":"FIFTYOFF","items":[{"productId":"1","quantity":2},{"productId":"3","quantity":1},{"productId":"1","quantity":1}]}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/order", strings.NewReader(body))
	req.Header.Set("api_key", "apitest")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var o order.Order
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		t.Fatal(err)
	}
	if len(o.ID) != 36 {
		t.Errorf("id %q is not a UUID", o.ID)
	}
	if len(o.Items) != 3 {
		t.Errorf("items = %d, want 3 (lines echoed as sent)", len(o.Items))
	}
	if len(o.Products) != 2 || o.Products[0].ID != "1" || o.Products[1].ID != "3" {
		t.Errorf("products = %+v, want unique [1 3] in first-seen order", o.Products)
	}
	if repo.Len() != 1 {
		t.Errorf("repo has %d orders, want 1", repo.Len())
	}
}
