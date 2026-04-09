module github.com/smplkit/go-sdk/logging/adapters/zap

go 1.24

require (
	github.com/smplkit/go-sdk v0.0.0
	go.uber.org/zap v1.27.0
)

require go.uber.org/multierr v1.10.0 // indirect

replace github.com/smplkit/go-sdk => ../../..
