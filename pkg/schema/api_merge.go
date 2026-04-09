package schema

import (
	"fmt"
	"strings"
)

// MergeAPISchemas combines two API schemas for the same route family.
// Existing schema identity is preserved so cache upgrades overwrite in place.
func MergeAPISchemas(existing, incoming *Schema) *Schema {
	if existing == nil {
		return incoming
	}
	if incoming == nil {
		return existing
	}

	merged := *existing
	merged.SchemaType = SchemaTypeAPI
	merged.Coverage = mergeCoverage(existing.Coverage, incoming.Coverage)
	if incoming.Session != nil {
		merged.Session = incoming.Session
	}
	if incoming.DiscoveredFrom != "" {
		merged.DiscoveredFrom = incoming.DiscoveredFrom
	}

	merged.Endpoints = mergeEndpoints(existing.Endpoints, incoming.Endpoints)
	normalizePrimary(merged.Endpoints)
	return &merged
}

func mergeCoverage(a, b string) string {
	switch {
	case a == SchemaCoverageComplete || b == SchemaCoverageComplete:
		return SchemaCoverageComplete
	case a == SchemaCoveragePartial || b == SchemaCoveragePartial:
		return SchemaCoveragePartial
	case b != "":
		return b
	default:
		return a
	}
}

func mergeEndpoints(existing, incoming []Endpoint) []Endpoint {
	merged := make([]Endpoint, 0, len(existing)+len(incoming))
	indexByKey := make(map[string]int, len(existing)+len(incoming))

	appendOrMerge := func(ep Endpoint) {
		key := endpointKey(ep)
		if idx, ok := indexByKey[key]; ok {
			merged[idx] = mergeEndpoint(merged[idx], ep)
			return
		}
		indexByKey[key] = len(merged)
		merged = append(merged, ep)
	}

	for _, ep := range existing {
		appendOrMerge(ep)
	}
	for _, ep := range incoming {
		appendOrMerge(ep)
	}

	return merged
}

func endpointKey(ep Endpoint) string {
	bodyTemplate := ""
	if ep.Body != nil {
		bodyTemplate = ep.Body.Template
	}
	return fmt.Sprintf("%s %s %s", strings.ToUpper(ep.Method), ep.URLTemplate, bodyTemplate)
}

func mergeEndpoint(existing, incoming Endpoint) Endpoint {
	merged := existing

	if merged.Name == "" || merged.Name == "directJSON" {
		merged.Name = incoming.Name
	}
	if merged.Description == "" || (merged.Description == merged.Name && incoming.Description != "") {
		merged.Description = incoming.Description
	}
	if incoming.Method != "" {
		merged.Method = incoming.Method
	}
	if incoming.URLTemplate != "" {
		merged.URLTemplate = incoming.URLTemplate
	}
	if merged.Headers == nil {
		merged.Headers = map[string]string{}
	}
	for k, v := range incoming.Headers {
		merged.Headers[k] = v
	}
	if len(merged.QueryParams) == 0 && len(incoming.QueryParams) > 0 {
		merged.QueryParams = incoming.QueryParams
	}
	if merged.Body == nil && incoming.Body != nil {
		merged.Body = incoming.Body
	}
	if len(merged.Variables) == 0 && len(incoming.Variables) > 0 {
		merged.Variables = incoming.Variables
	}
	if incoming.IsPrimary {
		merged.IsPrimary = true
	}
	if incoming.Confidence > merged.Confidence {
		merged.Confidence = incoming.Confidence
	}
	if merged.ResponseSchema == nil && incoming.ResponseSchema != nil {
		merged.ResponseSchema = incoming.ResponseSchema
	}
	if len(merged.ResponseMapping) == 0 && len(incoming.ResponseMapping) > 0 {
		merged.ResponseMapping = incoming.ResponseMapping
	}

	return merged
}

func normalizePrimary(endpoints []Endpoint) {
	if len(endpoints) == 0 {
		return
	}

	best := 0
	for i := range endpoints {
		endpoints[i].IsPrimary = false
		if endpoints[i].Confidence > endpoints[best].Confidence {
			best = i
		}
	}
	endpoints[best].IsPrimary = true
}
