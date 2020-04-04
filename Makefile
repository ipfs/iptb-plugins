IPTB_ROOT ?=$(HOME)/testbed

all: iptb

iptb:
	(cd iptb; go build)
CLEAN += iptb/iptb

ipfslocal:
	(cd local/plugin; go build -buildmode=plugin -o ../../build/localipfs.so)
CLEAN += build/localipfs.so

p2pdlocal:
	(cd localp2pd/plugin; go build -buildmode=plugin -o ../../build/localp2pd.so)
CLEAN += build/localp2pd.so

ipfsdocker:
	(cd docker/plugin; go build -buildmode=plugin -o ../../build/dockeripfs.so)
CLEAN += build/dockeripfs.so

ipfsbrowser:
	(cd browser/plugin; go build -buildmode=plugin -o ../../build/browseripfs.so)
CLEAN += build/browseripfs.so

install:
	(cd iptb; go install)

clean:
	rm ${CLEAN}

.PHONY: all clean ipfslocal p2pdlocal ipfsdocker ipfsbrowser iptb
