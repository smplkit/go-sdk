// Package smplkit provides a Go client for the smplkit platform.
//
// Quick start:
//
//	client, err := smplkit.NewClient(smplkit.Config{
//	    APIKey:      "sk_api_...",
//	    Environment: "production",
//	    Service:     "my-service",
//	})
//	cfg, err := client.Config().Get(ctx, "my-service")
//	if err != nil {
//	    var notFound *smplkit.SmplNotFoundError
//	    if errors.As(err, &notFound) {
//	        // handle not found
//	    }
//	    return err
//	}
//	fmt.Println(cfg.Name)
package smplkit
