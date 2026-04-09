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
			Description: "Update item quantities in the cart",
			Kind:        schema.ActionKindAPICall,
			Transport:   schema.ActionTransportAPICall,
			Method:      "POST",
			URLTemplate: origin + "/cart/update.js",
			Headers:     jsonHeaders,
			Params: []schema.ActionParam{
				{Name: "updates", In: "body", Type: "object", Required: true, Description: "Map of variant ID to quantity"},
			},
			Requirements: reqs,
			Confidence:   0.98,
			Source:       "shopify_ajax_api",
		},
		{
			Name:        "change_cart_item",
			Description: "Change quantity or remove a specific cart item",
			Kind:        schema.ActionKindAPICall,
			Transport:   schema.ActionTransportAPICall,
			Method:      "POST",
			URLTemplate: origin + "/cart/change.js",
			Headers:     jsonHeaders,
			Params: []schema.ActionParam{
				{Name: "id", In: "body", Type: "integer", Required: true, Description: "Variant ID or line item key"},
				{Name: "quantity", In: "body", Type: "integer", Required: true, Description: "New quantity (0 to remove)"},
			},
			Requirements: reqs,
			Confidence:   0.98,
			Source:       "shopify_ajax_api",
		},
	}
}
