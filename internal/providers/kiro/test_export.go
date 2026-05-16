package kiro

// TranslateRequestForTest exposes the package-private translateRequest
// function for cross-package integration tests. Production code calls
// translateRequest directly via Executor.ExecuteWithSession; the test
// shim avoids needing to set up a full executor + HTTP roundtrip just
// to assert how the body is shaped.
//
// Kept in its own file so it's obvious this exists only for tests —
// nothing in the production hot path should import from here.
func TranslateRequestForTest(model string, body []byte, profileARN string) ([]byte, error) {
	return translateRequest(model, body, profileARN)
}
