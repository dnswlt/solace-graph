.PHONY: proto

proto:
	protoc -I=proto \
	  --go_out=. \
	  --go_opt=module=github.com/dnswlt/solace-graph \
	  --go_opt=Mswcat/catalog/v1/catalog.proto=github.com/dnswlt/solace-graph/internal/catalog/pb \
	  swcat/catalog/v1/catalog.proto

