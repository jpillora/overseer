
### Issues

If you've found a bug, please create an [issue](https://github.com/jpillora/overseer/issues) or if possible, create a fix and send in a pull request.

## Contributing

If you'd like to contribute, please see the notes below and create an issue mentioning want you want to work on and if you're creating an addition to the core `overseer` repo, then also include the proposed API.

## Issues and bug fixes

### Tests

`overseer` needs a test which suite should drive an:

* HTTP client for verifying application version
* HTTP server for providing application upgrades
* an `overseer` process via `exec.Cmd`

And as it operates, confirm each phase.

### Updatable config

Child process should pass new config back to the main process and:
* Update logging settings
* Update socket bindings

---

## Extra Features

Below is a list of **optional extras** which would be nice to have. Optional is emphasised here because this project aims to maintain its small API surface.

### More fetchers

In general New fetchers should go in their own repos which can be linked to from the `overseer` docs. Arguably, the S3 fetcher should have been in it's own repo due to the size of the dependent package `github.com/aws/aws-sdk-go`, though this would break existing programs. Similarly, if a fetcher is reasonably simple and only uses the standard library then it could be included in this repo (e.g. the Github fetcher since it would just need `net/http`).

* HTTP fetcher long-polling (pseduo-push)
* SCP fetcher (connect to a server, poll path)
* Github fetcher (given a repo, poll releases)
* etcd fetcher (given a cluster, watch key)
* [Omaha](https://coreos.com/docs/coreupdate/custom-apps/coreupdate-protocol/) fetcher (a client which speaks omaha, downloads appropriate binary)

### Binary diffs and signatures

* There's two ways to implement binary upgrades:
  1. As as a stand-alone fetcher. The fetcher itself performs the binary merge to produce new binaries and just passes them to `overseer` as a complete binary (`io.Reader`).
  1. As a base feature. Create a binary format for delta upgrades, maybe something like:

    ```
    ["overseer-delta-upgrade" 22 bytes][(l)ength-of-config 4 bytes][JSON config l bytes][binary delta]
    ```

    The CLI would produce these delta upgrades and any fetcher could return one as a binary stream. Overseer just checks for the `"overseer-delta-upgrade"` string. The benefit of implementing this in overseer core is all fetchers would implicitly inherit this functionality.
* In **both** cases, each will need to ship with a corresponding CLI tool, this tool:
  * Must calculate and create delta updates using [binarydist](https://github.com/kr/binarydist)
    * [Courgette](http://dev.chromium.org/developers/design-documents/software-updates-courgette) would be better though not sure about Go compatibility
  * Optionally sign binaries
      * Create signing ECDSA private and private key
      * Store public and private keys on the build machine
      * Embed public keys into the binaries with

        ```
        -ldflags "-X github.com/jpillora/overseer/fetcher.PublicKey=A" -o myapp
        ```
      * Only accept future updates with binaries signed by the matching private key

### Versioning

* Originally, there was versioning in the API, though it added complexity and it was removed in favour of a simple binary `ID` which is just SHA1 of the binary.
* Local rollbacks might be handy though this requires some form of storage. At the moment, everything is inside the pre-existing binary so there is nothing to store.
