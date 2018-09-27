#!/bin/bash

set -e

if [ "$1" == "" ]; then echo Please pass a docset file name as an argument.; exit 1; fi

[ -d wscat ] || git clone https://github.com/websockets/wscat.git
cd wscat ; npm i ; cd ..

TITLE=$(basename $1 | cut -d'.' -f1)
PY=$(cat <<EOF
import base64, sys
print(base64.b64encode(sys.stdin.buffer.read()).decode())
EOF
)
ICON=$(tar -Oxf $1 $TITLE.docset/icon.png | python3 -c "$PY")

if [ "$ICON" == "" ]; then
    echo $(basename $1 | cut -d'.' -f1).docset/icon.png not found!
    exit 1
fi

echo Found $1 with title = $TITLE. Downloading...
LEN=$(wc -c $1 | awk '{print $1}')
curl -vvv -F "icon=${ICON}" -F "icon2x=${ICON}" -F "file=@$1" http://localhost:12340/item/local/${TITLE}/${LEN} &

node wscat/bin/wscat -o http://localhost/ --connect ws://localhost:12340/download_progress

echo "Success!"
