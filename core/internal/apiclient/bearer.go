package apiclient

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/retnd/retnd/core/apicontract"
)

// CallRoute performs the contract operation registered for a method and a
// route pattern ("GET", "/backup-sets/{source}/{set}"), and returns the
// response body undecoded.
//
// It is the door the MCP catalogue uses: that table is keyed by route
// pattern, as core/cliecho is, so it names a route and not an operation id.
// The route still has to be one the contract declares, and everything call
// does applies (the path is escaped per parameter, the answer is held to the
// contract's statuses and codes), so this is a second way to name an
// operation and not a way around the contract.
func (c *Client) CallRoute(ctx context.Context, method, route string, pathArgs []string, query url.Values, body json.RawMessage) (json.RawMessage, error) {
	var ep *apicontract.Endpoint
	for i := range apicontract.Endpoints {
		if apicontract.Endpoints[i].Method == method && apicontract.Endpoints[i].Path == route {
			ep = &apicontract.Endpoints[i]
			break
		}
	}
	if ep == nil {
		return nil, &ContractViolation{Operation: method + " " + route, Method: method, Reason: "api/v1/openapi.json declares no such route, so there is nothing to call"}
	}
	var payload any
	if len(body) > 0 {
		payload = body
	}
	var out json.RawMessage
	if err := c.callQuery(ctx, ep.ID, pathArgs, query, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}
