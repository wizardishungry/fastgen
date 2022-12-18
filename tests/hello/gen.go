package hello

//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue/1
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue sleep 1
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue echo yo
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue echo hello
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue sleep 5
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue sleep 5
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue sleep 5
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... /myQueue
