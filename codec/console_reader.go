package codec

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/NexusDAO/firehose-aleo/types"
	pbaleo "github.com/NexusDAO/firehose-aleo/types/pb/sf/aleo/type/v1"
	"github.com/golang/protobuf/proto"
	"github.com/streamingfast/bstream"
	"go.uber.org/zap"

	"io/ioutil"
	"gopkg.in/yaml.v2"
	"path/filepath"
	"runtime"
)

type Config struct {
	Start struct {
		Args []string `yaml:"args"`
		Flags struct {
			ReaderNodePath        string `yaml:"reader-node-path"`
			ReaderNodeArguments   string `yaml:"reader-node-arguments"`
			SubstreamsEnabled     bool   `yaml:"substreams-enabled"`
			SubstreamsClientEndpoint      string `yaml:"substreams-client-endpoint"`
			SubstreamsClientPlaintext     bool   `yaml:"substreams-client-plaintext"`
			SubstreamsPartialModeEnabled  bool   `yaml:"substreams-partial-mode-enabled"`
			SubstreamsSubRequestBlockRangeSize  int    `yaml:"substreams-sub-request-block-range-size"`
			SubstreamsCacheSaveInterval         int    `yaml:"substreams-cache-save-interval"`
			SubstreamsSubRequestParallelJobs    int    `yaml:"substreams-sub-request-parallel-jobs"`
		} `yaml:"flags"`
	} `yaml:"start"`
}

// ConsoleReader is what reads the `geth` output directly. It builds
// up some LogEntry objects. See `LogReader to read those entries .
type ConsoleReader struct {
	lines chan string
	close func()

	ctx  *parseCtx
	done chan interface{}

	logger *zap.Logger
}

func NewConsoleReader(logger *zap.Logger, lines chan string) (*ConsoleReader, error) {
	l := &ConsoleReader{
		lines:  lines,
		close:  func() {},
		done:   make(chan interface{}),
		logger: logger,
	}
	return l, nil
}

// todo: WTF?
func (r *ConsoleReader) Done() <-chan interface{} {
	return r.done
}

func (r *ConsoleReader) Close() {
	r.close()
}

type parsingStats struct {
	startAt  time.Time
	blockNum uint64
	data     map[string]int
	logger   *zap.Logger
}

func newParsingStats(logger *zap.Logger, block uint64) *parsingStats {
	return &parsingStats{
		startAt:  time.Now(),
		blockNum: block,
		data:     map[string]int{},
		logger:   logger,
	}
}

func (s *parsingStats) log() {
	s.logger.Info("reader block stats",
		zap.Uint64("block_num", s.blockNum),
		zap.Int64("duration", int64(time.Since(s.startAt))),
		zap.Reflect("stats", s.data),
	)
}

func (s *parsingStats) inc(key string) {
	if s == nil {
		return
	}
	k := strings.ToLower(key)
	value := s.data[k]
	value++
	s.data[k] = value
}

type parseCtx struct {
	currentBlock *pbaleo.Block
	stats        *parsingStats
	// height uint64
	logger *zap.Logger
}

func newContext(logger *zap.Logger, height uint64) *parseCtx {
	return &parseCtx{
		currentBlock: &pbaleo.Block{
			Transactions: []*pbaleo.Transactions{},
		},
		stats: newParsingStats(logger, height),

		logger: logger,
	}
}

func (r *ConsoleReader) ReadBlock() (out *bstream.Block, err error) {
	block, err := r.next()
	if err != nil {
		return nil, err
	}

	return types.BlockFromProto(block)
}

const (
	LogPrefix     = "FIRE"
	LogBlockStart = "BLOCK_START"
	LogHeader     = "BLOCK_HEADER"
	LogTrx        = "BLOCK_TRX"
	LogCoinbase   = "BLOCK_COINBASE"
	LogBlockEnd   = "BLOCK_END"
)

func (r *ConsoleReader) next() (out *pbaleo.Block, err error) {
	for line := range r.lines {
		if !strings.HasPrefix(line, LogPrefix) {
			continue
		}

		// This code assumes that distinct element do not contains space. This can happen
		// for example when exchanging JSON object (although we strongly discourage usage of
		// JSON, use serialized Protobuf object). If you happen to have spaces in the last element,
		// refactor the code here to avoid the split and perform the split in the line handler directly
		// instead.
		tokens := strings.Split(line[len(LogPrefix)+1:], " ")
		if len(tokens) < 2 {
			return nil, fmt.Errorf("invalid log line %q, expecting at least two tokens", line)
		}

		// Order the case from most occurring line prefix to least occurring
		switch tokens[0] {
		case LogBlockStart:
			err = r.blockBegin(tokens[1:])
		case LogHeader:
			err = r.ctx.headerAttr(tokens[1:])
		case LogTrx:
			err = r.ctx.trxBegin(tokens[1:])
		case LogCoinbase:
			err = r.ctx.coinbaseAttr(tokens[1:])
		case LogBlockEnd:
			// This end the execution of the reading loop as we have a full block here
			return r.ctx.readBlockEnd(tokens[1:])
		default:
			if r.logger.Core().Enabled(zap.DebugLevel) {
				r.logger.Debug("skipping unknown deep mind log line", zap.String("line", line))
			}
			continue
		}

		if err != nil {
			chunks := strings.SplitN(line, " ", 2)
			return nil, fmt.Errorf("%s: %w (line %q)", chunks[0], err, line)
		}
	}

	r.logger.Info("lines channel has been closed")
	return nil, io.EOF
}

func (r *ConsoleReader) processData(reader io.Reader) error {
	scanner := r.buildScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		r.lines <- line
	}

	if scanner.Err() == nil {
		close(r.lines)
		return io.EOF
	}

	return scanner.Err()
}

func (r *ConsoleReader) buildScanner(reader io.Reader) *bufio.Scanner {
	buf := make([]byte, 50*1024*1024)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(buf, 50*1024*1024)

	return scanner
}

// Format:
// FIRE BLOCK_START <height> <block_hash> <previous_hash> <signature>
func (r *ConsoleReader) blockBegin(params []string) error {
	if err := validateChunk(params, 4); err != nil {
		return fmt.Errorf("invalid log line length: %w", err)
	}

	blockHeight, err := strconv.ParseUint(params[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid block num: %w", err)
	}

	//Push new block meta
	r.ctx = newContext(r.logger, blockHeight)
	r.ctx.currentBlock.BlockHash = params[1]
	r.ctx.currentBlock.PreviousHash = params[2]
	r.logger.Info("block height:" + params[0])
	return nil
}

// Format:
// FIRE BLOCK_HEADER <sf.aleo.type.v1.header>
func (ctx *parseCtx) headerAttr(params []string) error {
	if err := validateChunk(params, 1); err != nil {
		return fmt.Errorf("invalid log line length: %w", err)
	}
	if ctx == nil {
		return fmt.Errorf("did not process a BLOCK_BEGIN")
	}

	out, err := base64.StdEncoding.DecodeString(params[0])
	if err != nil {
		return fmt.Errorf("read header in block: invalid base64 value: %w", err)
	}

	header := &pbaleo.Header{}
	if err := proto.Unmarshal(out, header); err != nil {
		return fmt.Errorf("read trx in block: invalid proto: %w", err)
	}
	// headerString := fmt.Sprintf("%v", header) // Convert *pbaleo.Header to string

	// ctx.logger.Info(headerString)
	ctx.currentBlock.Header = header
	return nil
}

// Format:
// FIRE BLOCK_TRX <sf.aleo.type.v1.transaction>
func (ctx *parseCtx) trxBegin(params []string) error {
	if err := validateChunk(params, 1); err != nil {
		return fmt.Errorf("invalid log line length: %w", err)
	}
	if ctx == nil {
		return fmt.Errorf("did not process a BLOCK_BEGIN")
	}

	out, err := base64.StdEncoding.DecodeString(params[0])
	if err != nil {
		return fmt.Errorf("read trx in bloc: invalid base64 value: %w", err)
	}

	transaction := &pbaleo.Transactions{}
	if err := proto.Unmarshal(out, transaction); err != nil {
		return fmt.Errorf("read trx in block: invalid proto: %w", err)
	}

	if len(ctx.currentBlock.Transactions) == 0 {
		ctx.logger.Info("received first transaction of block, ensuring its a valid first transaction")
	}

	ctx.currentBlock.Transactions = append(ctx.currentBlock.Transactions, transaction)

	return nil
}

// Format:
// FIRE BLOCK_COINBASE <sf.aleo.type.v1.coinbase>
func (ctx *parseCtx) coinbaseAttr(params []string) error {
	if err := validateChunk(params, 1); err != nil {
		return fmt.Errorf("invalid log line length: %w", err)
	}
	if ctx == nil {
		return fmt.Errorf("did not process a BLOCK_BEGIN")
	}

	out, err := base64.StdEncoding.DecodeString(params[0])
	if err != nil {
		return fmt.Errorf("read header in block: invalid base64 value: %w", err)
	}

	coinbase := &pbaleo.Coinbase{}
	if err := proto.Unmarshal(out, coinbase); err != nil {
		return fmt.Errorf("read trx in block: invalid proto: %w", err)
	}

	ctx.currentBlock.Coinbase = coinbase
	return nil
}

// Format:
// FIRE BLOCK_END <height>
func (ctx *parseCtx) readBlockEnd(params []string) (*pbaleo.Block, error) {
	if err := validateChunk(params, 1); err != nil {
		return nil, fmt.Errorf("invalid log line length: %w", err)
	}

	if ctx.currentBlock == nil {
		return nil, fmt.Errorf("current block not set")
	}

	blockHeight, err := strconv.ParseUint(params[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse blockNum: %w", err)
	}
	if blockHeight != ctx.stats.blockNum {
		return nil, fmt.Errorf("end block height does not match active block height, got block height %d but current is block height %d", blockHeight, ctx.stats.blockNum)
	}

	ctx.logger.Info("console reader read block",
		zap.Uint64("height", ctx.stats.blockNum),
		zap.String("hash", ctx.currentBlock.BlockHash),
		zap.String("prev_hash", ctx.currentBlock.PreviousHash),
		zap.Int("trx_count", len(ctx.currentBlock.Transactions)),
	)

	err = change_height(fmt.Sprintf("%d", blockHeight-1))

	return ctx.currentBlock, err
}

func validateChunk(params []string, count int) error {
	if len(params) != count {
		return fmt.Errorf("%d fields required but found %d", count, len(params))
	}
	return nil
}

func change_height(height string) error {
	// 获取当前源文件的路径
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("failed to get current file path")
	}

	// 计算相对路径
	currentDir := filepath.Dir(filepath.Dir(filename))
	filePath := filepath.Join(currentDir, "devel/standard/standard.yaml")
	// 读取 standard.yaml 文件内容
	// filePath := "../devel/standard/standard.yaml"
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %v", err)
	}

	// 解析 YAML 文件内容
	config := Config{}
	err = yaml.Unmarshal(content, &config)
	if err != nil {
		return fmt.Errorf("failed to parse YAML: %v", err)
	}

	// 修改 reader-node-arguments 参数
	args := strings.Fields(config.Start.Flags.ReaderNodeArguments)
	for i := 0; i < len(args); i++ {
		if args[i] == "+-s" && i+1 < len(args) {
			args[i+1] = height // 在这里修改 -s 参数的值
			break
		}
	}
	modifiedReaderNodeArguments := strings.Join(args, " ")
	config.Start.Flags.ReaderNodeArguments = modifiedReaderNodeArguments

	// 将修改后的内容转换回 YAML 格式
	modifiedContent, err := yaml.Marshal(&config)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %v", err)
	}

	// 将修改后的内容写入 standard.yaml 文件
	err = ioutil.WriteFile(filePath, modifiedContent, 0644)
	if err != nil {
		return fmt.Errorf("failed to write file: %v", err)
	}

	fmt.Println("Successfully modified and saved the file.")
	return nil
}
