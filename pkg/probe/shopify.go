package probe

import (
	"context"
	"net/url"
	"time"

	"github.com/hermai-ai/hermai-cli/internal/httpclient"
	"github.com/hermai-ai/hermai-cli/pkg/schema"
)

// tryShopify detects Shopify stores by probing /products.json.
// Works on any of Shopify's ~4.8M stores regardless of custom domain.
func tryShopify(ctx context.Context, client httpclient.Doer, targetURL string) (*schema.Schema, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}

	origin := parsed.Scheme + "://" + parsed.Host
	productsURL := origin + "/products.json"

	body, err := doJSONRequest(ctx, client, productsURL, nil)
	if err != nil || body == nil {
		return nil, err
	}

	// Validate Shopify shape: must have "products" array
	root, ok := body.(map[string]any)
	if !ok {
		return nil, nil
	}
	products, ok := root["products"]
	if !ok {
		return nil, nil
	}
	if _, isArr := products.([]any); !isArr {
		return nil, nil
	}

	return buildShopifySchema(targetURL, origin), nil
}

func buildShopifySchema(targetURL, origin string) *schema.Schema {
	parsed, _ := url.Parse(targetURL)
	domain := parsed.Hostname()

	return &schema.Schema{
		ID:             schema.GenerateID(domain, "/products"),
		Domain:         domain,
		URLPattern:     "/products",
		SchemaType:     schema.SchemaTypeAPI,
		Coverage:       schema.SchemaCoverageComplete,
		Version:        1,
		CreatedAt:      time.Now(),
		DiscoveredFrom: targetURL,
		Endpoints: []schema.Endpoint{
			{
				Name:        "shopify_products",
				Description: "Shopify store product catalog from the public AJAX API",
				Method:      "GET",
				URLTemplate: origin + "/products.json",
				Headers:     map[string]string{},
				IsPrimary:   true,
				Confidence:  0.98,
			},
			{
				Name:        "shopify_product",
				Description: "Shopify single product detail from the public AJAX API",
				Method:      "GET",
				URLTemplate: origin + "/products/{handle}.json",
				Headers:     map[string]string{},
				Variables: []schema.Variable{
					{Name: "handle", Source: "path", Pattern: "1"},
				},
				Confidence: 0.95,
			},
		},
		Actions: shopifyActions(origin),
	}
}

func shopifyActions(origin string) []schema.Action {
	jsonHeaders := map[string]string{"Content-Type": "application/json"}
	reqs := schema.ActionRequirements{
		Auth:    schema.ActionAuthPublicOnly,
		Session: schema.ActionSessionCookies,
		JS:      schema.ActionJSNotRequired,
	}

	return []schema.Action{
		{
			Name:        "add_to_cart",
			Description: "Add a product variant to the shopping cart",
			Kind:        schema.ActionKindAPICall,
			Transport:   schema.ActionTransportAPICall,
			Method:      "POST",
			URLTemplate: origin + "/cart/add.js",
			Headers:     jsonHeaders,
			Params: []schema.ActionParam{
				{Name: "id", In: "body", Type: "integer", Required: true, Description: "Product variant ID"},
				{Name: "quantity", In: "body", Type: "integer", Required: true, Default: "1", Description: "Quantity to add"},
			},
			Requirements: reqs,
			Confidence:   0.98,
			Source:       "shopify_ajax_api",
		},
		{
			Name:        "get_cart",
			Description: "Get current cart contents",
			Kind:        schema.ActionKindAPICall,
			Transport:   schema.ActionTransportAPICall,
			Method:      "GET",
			URLTemplate: origin + "/cart.js",
			Headers:     map[string]string{},
			Requirements: reqs,
			Confidence:   0.98,
			Source:       "shopify_ajax_api",
		},
		{
			Name:        "update_cart",
			Description: "Update item quantities in the cart by variant ID. Pass {variant_id: new_qty} for each line you want to change; setting qty=0 removes that line. This is the simplest write path because it works directly with the variant IDs you already have from the products endpoint — no need to call get_cart first. Prefer this over change_cart_item unless you specifically need to operate on a single line by its position.",
			Kind:        schema.ActionKindAPICall,
			Transport:   schema.ActionTransportAPICall,
			Method:      "POST",
			URLTemplate: origin + "/cart/update.js",
			Headers:     jsonHeaders,
			Params: []schema.ActionParam{
				{Name: "updates", In: "body", Type: "object", Required: true, Description: "Map of variant ID to quantity, e.g. {\"41397031600208\": 2, \"41397031600209\": 0}"},
			},
			Requirements: reqs,
			Confidence:   0.98,
			Source:       "shopify_ajax_api",
		},
		{
			Name:        "change_cart_item",
			Description: "Change quantity or remove a single cart line by its line key OR 1-indexed line position. Functionally a subset of update_cart — only useful when you need to operate on one line at a time and have its line key from get_cart. Most agents should prefer update_cart, which works with variant IDs directly and avoids the extra get_cart round trip. Note: passing a variant ID here will fail with 400 — Shopify expects either the line key (e.g. \"41397031600208:hex32\") or the line index (1, 2, ...).",
			Kind:        schema.ActionKindAPICall,
			Transport:   schema.ActionTransportAPICall,
			Method:      "POST",
			URLTemplate: origin + "/cart/change.js",
			Headers:     jsonHeaders,
			Params: []schema.ActionParam{
				{Name: "id", In: "body", Type: "string", Required: true, Description: "Either a cart line key from get_cart (format \"variant_id:hex32\", e.g. \"41397031600208:5fdf0a35f653145ae11c2ad58ffea3f4\") or a 1-indexed line position. NOT the variant ID — that returns HTTP 400."},
				{Name: "quantity", In: "body", Type: "integer", Required: true, Description: "New quantity (0 to remove the line)"},
			},
			Requirements: reqs,
			Confidence:   0.98,
			Source:       "shopify_ajax_api",
		},
	}
}
