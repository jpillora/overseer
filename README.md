# overseer

[![GoDoc](https://godoc.org/github.com/jpillora/overseer?status.svg)](https://godoc.org/github.com/jpillora/overseer)

Monitorable, gracefully restarting, self-upgrading binaries in Go (golang)

The main goal of this project is to facilitate the creation of self-upgrading binaries which play nice with standard process managers. The secondary goal is user simplicity. :warning: This is beta software.

### Features

* Simple
* Works with process managers
* Graceful, zero-down time restarts
* Easy self-upgrading binaries

### Install

```sh
go get github.com/jpillora/overseer
```

### Quick example

``` go
package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jpillora/overseer"
	"github.com/jpillora/overseer/fetcher"
)

//create another main() to run the overseer process
//and then convert your old main() into a 'prog(state)'
func main() {
	overseer.Run(overseer.Config{
		Program: prog,
		Address: ":3000",
		Fetcher: &fetcher.HTTP{
			URL:      "http://localhost:4000/binaries/myapp",
			Interval: 1 * time.Second,
		},
		// Log: true, //display log of overseer actions
	})
}

//prog(state) runs in a child process
func prog(state overseer.State) {
	log.Printf("app (%s) listening...", state.ID)
	http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "app (%s) says hello\n", state.ID)
	}))
	http.Serve(state.Listener, nil)
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

### More examples

* Only use graceful restarts

	```go
	func main() {
		overseer.Run(overseer.Config{
			Program: prog,
			Address: ":3000",
		})
	}
	```

	Send `main` a `SIGUSR2` to manually trigger a restart

* Only use auto-upgrades, no restarts

	```go
	func main() {
		overseer.Run(overseer.Config{
			Program: prog,
			NoRestartAfterFetch: true
			Fetcher: &fetcher.HTTP{
				URL:      "http://localhost:4000/binaries/myapp",
				Interval: 1 * time.Second,
			},
		})
	}
	```

	Your binary will be upgraded though it will require manual restart from the user

### Warnings

* Currently shells out to `mv` for moving files because `mv` handles cross-partition moves unlike `os.Rename`.
* Bind `Addresses` can only be changed by restarting the main process.
* Only supported on darwin and linux.

### Documentation

* [Core `overseer` package](https://godoc.org/github.com/jpillora/overseer)
* [Common `fetcher.Interface`](https://godoc.org/github.com/jpillora/overseer/fetcher#Interface)
* [HTTP fetcher type](https://godoc.org/github.com/jpillora/overseer/fetcher#HTTP)
* [S3 fetcher type](https://godoc.org/github.com/jpillora/overseer/fetcher#S3)

### Architecture overview

* `overseer` uses the main process to check for and install upgrades and a child process to run `Program`
* All child process pipes are connected back to the main process
* All signals received on the main process are forwarded through to the child process
* The provided `fetcher.Interface` will be used to `Fetch()` the latest build of the binary
* The `fetcher.HTTP` accepts a `URL`, it polls this URL with HEAD requests and until it detects a change. On change, we `GET` the `URL` and stream it back out to `overseer`.
* Once a binary is received, it is run with a simple echo token to confirm it is a `overseer` binary.
* Except for scheduled upgrades, the child process exiting will cause the main process to exit with the same code. So, **`overseer` is not a process manager**.

### Docker

1. Compile your `overseer`able `app` to a `/path/on/docker/host/dir/app`
1. Then run it with:

	```sh
	docker run -d -v /path/on/docker/host/dir/:/home/ -w /home/ debian  -w /home/app
	```

1. For testing, swap out `-d` (daemonize) for `--rm -it` (remove on exit, input, terminal)
1. `app` can use the current working directory as storage
1. `debian` doesn't ship with TLS certs, you can mount them in with `-v /etc/ssl/certs/ca-certificates.crt:/etc/ssl/certs/ca-certificates.crt`

### Alternatives

* https://github.com/sanbornm/go-selfupdate
* https://github.com/inconshreveable/go-update

### TODO

* Log levels
* Github fetcher (given a repo, poll releases)
* etcd fetcher (given a cluster, watch key)
* `overseer` CLI tool ([TODO](cmd/overseer/TODO.md))
* `overseer` package
	* Execute and verify calculated delta updates with https://github.com/kr/binarydist
	* [Omaha](https://coreos.com/docs/coreupdate/custom-apps/coreupdate-protocol/) client support
