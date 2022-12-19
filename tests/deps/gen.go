package hello

//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue/1
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue sleep 5
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... myQueue echo myQueue done
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... waiter/1/myQueue
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... waiter echo in second queue
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... /myQueue
//go:generate go run jonwillia.ms/fastgen/cmd/fastgen/... /waiter
