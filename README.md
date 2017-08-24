# overseer

[![GoDoc](https://godoc.org/github.com/jpillora/overseer?status.svg)](https://godoc.org/github.com/jpillora/overseer)

`overseer` is a package for creating monitorable, gracefully restarting, self-upgrading binaries in Go (golang). The main goal of this project is to facilitate the creation of self-upgrading binaries which play nice with standard process managers, secondly it should expose a small and simple API with reasonable defaults.

![overseer diagram](https://docs.google.com/drawings/d/1o12njYyRILy3UDs2E6JzyJEl0psU4ePYiMQ20jiuVOY/pub?w=566&h=284)

Commonly, graceful restarts are performed by the active process (*dark blue*) closing its listeners and passing these matching listening socket files (*green*) over to a newly started process. This restart causes any **foreground** process monitoring to incorrectly detect a program crash. `overseer` attempts to solve this by using a small process to perform this socket file exchange and proxying signals and exit code from the active process.

### Features

* Simple
* Works with process managers (systemd, upstart, supervisor, etc)
* Graceful, zero-down time restarts
* Easy self-upgrading binaries

### Install

```sh
go get github.com/jpillora/overseer
```

### Quick example

This program works with process managers, supports graceful, zero-down time restarts and self-upgrades its own binary.

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

**How it works:**

* `overseer` uses the main process to check for and install upgrades and a child process to run `Program`.
* The main process retrieves the files of the listeners described by `Address/es`.
* The child process is provided with these files which is converted into a `Listener/s` for the `Program` to consume.
* All child process pipes are connected back to the main process.
* All signals received on the main process are forwarded through to the child process.
* `Fetcher` runs in a goroutine and checks for updates at preconfigured interval. When `Fetcher` returns a valid binary stream (`io.Reader`), the master process saves it to a temporary location, verifies it, replaces the current binary and initiates a graceful restart.
* The `fetcher.HTTP` accepts a `URL`, it polls this URL with HEAD requests and until it detects a change. On change, we `GET` the `URL` and stream it back out to `overseer`. See also `fetcher.S3`.
* Once a binary is received, it is run with a simple echo token to confirm it is a `overseer` binary.
* Except for scheduled restarts, the active child process exiting will cause the main process to exit with the same code. So, **`overseer` is not a process manager**.

See [Config](https://godoc.org/github.com/jpillora/overseer#Config)uration options [here](https://godoc.org/github.com/jpillora/overseer#Config) and the runtime [State](https://godoc.org/github.com/jpillora/overseer#State) available to your program [here](https://godoc.org/github.com/jpillora/overseer#State).

### More examples

See the [example/](example/) directory and run `example.sh`, you should see the following output:

```sh
$ cd example/
$ sh example.sh
BUILT APP (1)
RUNNING APP
app#1 (031c802ee74f00b2a5c52f2fe647523973c09441) listening...
app#1 (031c802ee74f00b2a5c52f2fe647523973c09441) says hello
app#1 (031c802ee74f00b2a5c52f2fe647523973c09441) says hello
BUILT APP (2)
app#2 (25d19f139f50f39fadbd066b438ebdc28d818eb1) listening...
app#2 (25d19f139f50f39fadbd066b438ebdc28d818eb1) says hello
app#2 (25d19f139f50f39fadbd066b438ebdc28d818eb1) says hello
app#1 (031c802ee74f00b2a5c52f2fe647523973c09441) says hello
app#1 (031c802ee74f00b2a5c52f2fe647523973c09441) exiting...
BUILT APP (3)
app#3 (5ed8170e5bbd6947cc514c87ac29e7acfba5cffc) listening...
app#3 (5ed8170e5bbd6947cc514c87ac29e7acfba5cffc) says hello
app#3 (5ed8170e5bbd6947cc514c87ac29e7acfba5cffc) says hello
app#2 (25d19f139f50f39fadbd066b438ebdc28d818eb1) says hello
app#2 (25d19f139f50f39fadbd066b438ebdc28d818eb1) exiting...
app#3 (5ed8170e5bbd6947cc514c87ac29e7acfba5cffc) says hello
```

**Note:** `app#1` stays running until the last request is closed.

#### Only use graceful restarts

```go
func main() {
	overseer.Run(overseer.Config{
		Program: prog,
		Address: ":3000",
	})
}
```

Send `main` a `SIGUSR2` (`Config.RestartSignal`) to manually trigger a restart

#### Only use auto-upgrades, no restarts

```go
func main() {
	overseer.Run(overseer.Config{
		Program: prog,
		NoRestart: true,
		Fetcher: &fetcher.HTTP{
			URL:      "http://localhost:4000/binaries/myapp",
			Interval: 1 * time.Second,
		},
	})
}
```

Your binary will be upgraded though it will require manual restart from the user, suitable for creating self-upgrading command-line applications.

#### Multi-platform binaries using a dynamic fetch `URL`

```go
func main() {
	overseer.Run(overseer.Config{
		Program: prog,
		Fetcher: &fetcher.HTTP{
			URL: "http://localhost:4000/binaries/app-"+runtime.GOOS+"-"+runtime.GOARCH,
			//e.g.http://localhost:4000/binaries/app-linux-amd64
		},
	})
}
```

### Known issues

* The master process's `overseer.Config` cannot be changed via an upgrade, the master process must be restarted.
	* Therefore, `Addresses` can only be changed by restarting the main process.
* Currently shells out to `mv` for moving files because `mv` handles cross-partition moves unlike `os.Rename`.
* Only supported on darwin and linux.
* Package `init()` functions will run twice on start, once in the main process and once in the child process.

### More documentation

* [Core `overseer` package](https://godoc.org/github.com/jpillora/overseer)
* [Common `fetcher.Interface`](https://godoc.org/github.com/jpillora/overseer/fetcher#Interface)
	* [File fetcher](https://godoc.org/github.com/jpillora/overseer/fetcher#File)
	* [HTTP fetcher](https://godoc.org/github.com/jpillora/overseer/fetcher#HTTP)
	* [S3 fetcher](https://godoc.org/github.com/jpillora/overseer/fetcher#S3)
	* [Github fetcher](https://godoc.org/github.com/jpillora/overseer/fetcher#Github)

### Third-party Fetchers

* [overseer-bindiff](https://github.com/tgulacsi/overseer-bindiff) A binary diff fetcher and builder

### Docker

1. Compile your `overseer`able `app` to a `/path/on/docker/host/dir/app`
1. Then run it with:

	```sh
	#run the app inside a standard Debian container
	docker run -d -v /path/on/docker/host/dir/:/home/ -w /home/ debian /home/app
	```

1. For testing, swap out `-d` (daemonize) for `--rm -it` (remove on exit, input, terminal)
1. `app` should mount its parent directory as a volume in order to store the latest binaries on the host
1. If the OS doesn't ship with TLS certs, you can mount them from the host with `-v /etc/ssl/certs/ca-certificates.crt:/etc/ssl/certs/ca-certificates.crt`

### Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md)
