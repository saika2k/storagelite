#!/usr/bin/env bash

./lotus fetch-params 8MiB
./lotus-seed pre-seal --sector-size 8MiB --num-sectors 2
./lotus-seed genesis new localnet.json
./lotus-seed genesis add-miner localnet.json ~/.genesis-sectors/pre-seal-t01000.json
tmux new-session -s "lotus-daemon" -d "./lotus daemon --lotus-make-genesis=devgen.car --genesis-template=localnet.json --bootstrap=false"
echo "sleep 30s" && sleep 30s
./lotus wallet import --as-default ~/.genesis-sectors/pre-seal-t01000.key
./lotus-miner init --genesis-miner --actor=t01000 --sector-size=8MiB --pre-sealed-sectors=~/.genesis-sectors --pre-sealed-metadata=~/.genesis-sectors/pre-seal-t01000.json --nosync
tmux new-session -s "lotus-miner" -d "./lotus-miner run --nosync"
