# go-upgrade

[![GoDoc](https://godoc.org/github.com/jpillora/go-upgrade?status.svg)](https://godoc.org/github.com/jpillora/go-upgrade)

Daemonizable self-upgrading binaries in Go (golang).

The main goal of this project is to facilitate the creation of self-upgrading binaries which play nice with standard process managers. The secondary goal is user simplicity. :warning: This is beta software.

### Install

```
go get github.com/jpillora/go-upgrade
```

### Quick Usage

``` go
package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jpillora/go-upgrade"
	"github.com/jpillora/go-upgrade/fetcher"
)

//convert your 'main()' into a 'prog(state)'
//'prog()' is run in a child process
func prog(state upgrade.State) {
	log.Printf("app (%s) listening...", state.ID)
	http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "app (%s) says hello\n", state.ID)
	}))
	http.Serve(state.Listener, nil)
}

//then create another 'main()' which runs the upgrades
//'main()' is run in the initial process
func main() {
	upgrade.Run(upgrade.Config{
		Program: prog,
		Address: ":3000",
		Fetcher: &fetcher.HTTP{
			URL:      "http://localhost:4000/binaries/myapp",
			Interval: 1 * time.Second,
		},
		// Log: false, //display log of go-upgrade actions
	})
}
```

```sh
$ cd example/
$ sh example.sh
serving . on port 4000
BUILT APP (1)
RUNNING APP
app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) listening...
app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) says hello
app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) says hello
BUILT APP (2)
app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) listening...
app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) says hello
app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) says hello
app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) says hello
app#1 (96015cccdebcec119adad34f49b93e02552f3ad9) exiting...
BUILT APP (3)
app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) listening...
app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) says hello
app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) says hello
app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) says hello
app#2 (ccc073a1c8e94fd4f2d76ebefb2bbc96790cb795) exiting...
app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) says hello
app#3 (286848c2aefcd3f7321a65b5e4efae987fb17911) exiting...
```

### Documentation

* [Core `upgrade` package](https://godoc.org/github.com/jpillora/go-upgrade)
* [Common `fetcher.Interface`](https://godoc.org/github.com/jpillora/go-upgrade/fetcher#Interface)
* [Basic `fetcher.HTTP` fetcher type](https://godoc.org/github.com/jpillora/go-upgrade/fetcher#HTTP)

### Architecture overview

*. `go-upgrade` uses the main process to check for and install upgrades and a child process to run `Program`
*. All child process pipes are connected back to the main process
*. All signals received on the main process are forwarded through to the child process
*. The provided `fetcher.Interface` will be used to `Fetch()` the latest build of the binary
*. The `fetcher.HTTP` accepts a `URL`, it polls this URL with HEAD requests and until it detects a change. On change, we `GET` the `URL` and stream it back out to `go-upgrade`.
*. Once a binary is received, it is run with a simple echo token to confirm it is a `go-upgrade` binary.
* Except for scheduled upgrades, the child process exiting will cause the main process to exit with the same code. So, **`go-upgrade` is not a process manager**.

### Alternatives

* https://github.com/sanbornm/go-selfupdate
* https://github.com/inconshreveable/go-update

### TODO

* Github fetcher (given a repo)
* S3 fetcher (given a bucket and credentials)
* etcd fetcher (given a cluster, watch key)
* `go-upgrade` CLI tool
	* Calculate delta updates with https://github.com/kr/binarydist ([courgette](http://dev.chromium.org/developers/design-documents/software-updates-courgette) would be nice)
	* Signed binaries and updates *(use HTTPS where in the meantime)*
		* Create signing ECDSA private and private key, store locally
		* Build binaries and include public key with `-ldflags "-X github.com/jpillora/go-upgrade/fetcher.PublicKey=A" -o myapp`
		* Only accept future updates with binaries signed by the matching private key
* `upgrade` package
	* Execute and verify calculated delta updates with https://github.com/kr/binarydist
	* [Omaha](https://coreos.com/docs/coreupdate/custom-apps/coreupdate-protocol/) client support
