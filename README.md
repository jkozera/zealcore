## PoC usage

First, clone zealcore into `$GOPATH/src/github.com/zealdocs/zealcore`.

```
$ go build
$ ./zealcore
```

And in separate terminal, to download some docset (requires the `jq` tool to be installed):

```
$ ./download_docset.sh [docsetname]
```

for example,

```
$ ./download_docset.sh AngularJS
```

This should create a `.zealdocset` file in your current directory.

After it's done (which can take long time for large docsets), kill zealcore, restart it (which is necessary because adding freshly downloaded/extracted docsets to index is not supported yet), and go to http://localhost:12340/
