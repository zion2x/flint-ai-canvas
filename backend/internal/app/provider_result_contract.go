package app

// providerResultContractError marks validation failures after a successful provider call.
// The task must fail without another generation or an automatic billing refund.
type providerResultContractError struct {
	Err error
}

func (e *providerResultContractError) Error() string {
	return e.Err.Error()
}

func (e *providerResultContractError) Unwrap() error {
	return e.Err
}
