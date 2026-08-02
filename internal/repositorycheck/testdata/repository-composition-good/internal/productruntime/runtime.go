package productruntime

func Start(options any) (any, error) {
	return startWithBuilders(options, productionBuilders())
}

func productionBuilders() any                 { return nil }
func startWithBuilders(any, any) (any, error) { return nil, nil }
