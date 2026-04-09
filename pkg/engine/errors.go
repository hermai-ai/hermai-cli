package engine

import "errors"

var (
	ErrAuthRequired   = errors.New("hermai: page requires authentication")
	ErrAnalysisFailed = errors.New("hermai: LLM failed to extract schema")
	ErrNoEndpoints    = errors.New("hermai: no API endpoints discovered")
)
