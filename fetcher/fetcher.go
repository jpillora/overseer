package fetcher

import "io"

type Interface interface {
	//Fetch should check if there is an updated
	//binary to fetch, and then stream it back the
	//form of an io.Reader. If io.Reader is nil,
	//then it is assumed there are no updates.
	Fetch() (io.Reader, error)
}

//Converts a fetch function into interface
func Func(fn func() (io.Reader, error)) Interface {
	return &fetcher{fn}
}

type fetcher struct {
	fn func() (io.Reader, error)
}

func (f fetcher) Fetch() (io.Reader, error) {
	return f.fn()
}
