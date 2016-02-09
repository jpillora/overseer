### Placeholder for the `overseer` binary tool

* Calculate delta updates with https://github.com/kr/binarydist ([courgette](http://dev.chromium.org/developers/design-documents/software-updates-courgette) would be nice)
* Signed binaries and updates *(use HTTPS where in the meantime)*
    * Create signing ECDSA private and private key, store locally
    * Build binaries and include public key with `-ldflags "-X github.com/jpillora/overseer/fetcher.PublicKey=A" -o myapp`
    * Only accept future updates with binaries signed by the matching private key
