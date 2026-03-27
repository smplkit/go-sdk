// Package smplkit provides a Go client for the smplkit platform.
//
// The SDK follows a two-layer architecture: auto-generated types live in
// internal/generated, while this package provides the hand-crafted public API.
//
// Quick start:
//
//	client := smplkit.NewClient("sk_api_...")
//	cfg, err := client.Config().GetByKey(ctx, "my-service")
//	if err != nil {
//	    var notFound *smplkit.SmplNotFoundError
//	    if errors.As(err, &notFound) {
//	        // handle not found
//	    }
//	    return err
//	}
//	fmt.Println(cfg.Name)
package smplkit
