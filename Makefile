GOOS ?= $(shell uname -s | tr '[:upper:]' '[:lower:]')
GOARCH ?= $(shell uname -m)

ifeq ($(GOARCH),x86_64)
    GOARCH = amd64
endif

.PHONY: all clean

all:
	mkdir -p ./build
	go build -o ./build/gotun-$(GOOS)-$(GOARCH) .

clean:
	rm -rf ./build
