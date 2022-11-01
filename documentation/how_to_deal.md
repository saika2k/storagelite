# How to make a deal in FileDAG network

## Import the local file

```shell
./lotus client import hello.txt
```

Then you get the CID of `hello.txt`.

## Create a deal（interactively）

```shell
./lotus client deal
```

1. Specify the CID of the payload you want to backup on FileDAG.
2. Enter the number of days you want to keep this file on FileDAG. Enter 60.
3. Tell Lotus whether or not this is a Filecoin Plus deal. It is not.
4. Enter the miner IDs. Multiple IDs should be separated by spaces.
5. Confirm your transaction by entering `yes`.

Then you get Deal CIDs.

## Create a deal (non-interactively, recommended)

```shell
./lotus client deal [dataCid miner price duration]
```
