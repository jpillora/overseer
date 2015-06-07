package upgrade

type Fetcher interface {
	Fetch(currentVersion string) (binary []byte, err error)
}
