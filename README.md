# Firehose on Aleo Blockchain
[![reference](https://img.shields.io/badge/godoc-reference-5272B4.svg?style=flat-square)](https://pkg.go.dev/github.com/NexusDAO/firehose-aleo)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

# Usage

## Setup

1. Build the `aleo-sync-streamer` binary and bind `aleo-sync-streamer` to the `PATH`
1. Run firehose
    ```
    ./devel/standard/start.sh -c
    ```

    And ensure blocks are flowing:

    ```
    grpcurl -plaintext -d '{"start_block_num": 10, "stop_block_num": 20}' localhost:18015 sf.firehose.v2.Stream.Blocks
    ```

## Re-generate Protobuf Definitions

1. Ensure that `protoc` is installed:
   ```
   brew install protoc
   ```

1. Ensure that `protoc-gen-go` and `protoc-gen-go-grpc` are installed and at the correct version
    ```
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.25.0
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.1.0
    ```

1. Generate go file from modified protobuf

   ```
   ./types/pb/generate.sh
   mv ./types/pb/type.pb.go ./types/pb/sf/aleo/type/v1
   ```

1. Run tests and fix any problems:

    ```
    ./bin/test.sh
    ```

1. Run `standard` config to ensure everything is working as expected:

    ```
    ./devel/standard/start.sh -c
    ```
    or
    ```
    nohup ./devel/standard/start.sh -c &
    ```  

    And ensure blocks are flowing:

    ```
    grpcurl -plaintext -d '{"start_block_num": 10, "stop_block_num": 20}' localhost:18015 sf.firehose.v2.Stream.Blocks
    ```

1. (optional) Make .spkg file for substreams
    ```substreams pack substreams/substreams.yaml```

## License

[Apache 2.0](LICENSE)