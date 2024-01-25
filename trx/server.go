package trx

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/shengdoushi/base58"
	"github.com/ubtr/ubt-go/agent"
	"github.com/ubtr/ubt-go/blockchain"
	"github.com/ubtr/ubt-go/blockchain/trx"
	rpcerrors "github.com/ubtr/ubt-go/commons/errors"
	"github.com/ubtr/ubt-go/eth/contracts/erc20"
	"github.com/ubtr/ubt-go/eth/server"
	"github.com/ubtr/ubt/go/api/proto"
	"github.com/ubtr/ubt/go/api/proto/services"
)

const ERC20_FEE_LIMIT = 20000000

func init() {
	agent.AgentFactories[trx.CODE_STR] = func(ctx context.Context, config *agent.ChainConfig) agent.UbtAgent {
		return InitServer(ctx, config)
	}
}

var TrxExtensions = server.Extensions{
	AddressFromString: func(address string) (common.Address, error) {
		addrB58, err := base58.Decode(address, base58.BitcoinAlphabet)
		if err != nil {
			return common.Address{}, err
		}
		addrB58 = addrB58[:len(addrB58)-4]
		return common.BytesToAddress(addrB58[len(addrB58)-20:]), nil
	},
	AddressToString: func(address common.Address) string {
		addressTron := make([]byte, 0)
		addressTron = append(addressTron, trx.TronBytePrefix)
		addressTron = append(addressTron, address.Bytes()...)
		return trx.Address(addressTron).String()
	},
}

func InitServer(ctx context.Context, config *agent.ChainConfig) *TrxAgent {
	agent := &TrxAgent{
		EthServer: *server.InitServer(ctx, config),
	}
	if config.HttpUrls == nil || len(config.HttpUrls) == 0 || config.HttpUrls[0].Url == "" {
		agent.Log.Warn("no http url provided - trx requires http api to create/sign txs")
	} else {
		agent.client = NewTrxApiClient(config.HttpUrls[0].Url, agent.Log)
	}

	agent.EthServer.Extensions = TrxExtensions

	return agent
}

type TrxAgent struct {
	server.EthServer
	client *TrxApiClient
}

func (srv *TrxAgent) CreateTransfer(ctx context.Context, req *services.CreateTransferRequest) (*services.TransactionIntent, error) {
	srv.Log.Info("CreateTransfer", "req", req)
	if srv.client == nil {
		return nil, errors.ErrUnsupported
	}

	curId, err := blockchain.UChainCurrencyIdromString(req.Amount.CurrencyId)
	if err != nil {
		return nil, err
	}

	if curId.IsNative() {

		res, err := srv.client.CreateTransaction(ctx, CreateTransactionRequest{
			OwnerAddress: req.From,
			ToAddress:    req.To,
			Amount:       1,
			Visible:      true,
		})

		if err != nil {
			return nil, err
		}

		if res.Error != "" {
			return nil, errors.New(res.Error)
		}

		if err != nil {
			return nil, err
		}

		return &services.TransactionIntent{
			Id:            common.Hex2Bytes(res.TxId),
			SignatureType: trx.Instance.SignatureType,
			PayloadToSign: common.Hex2Bytes(res.TxId),
			RawData:       res.RawData,
			EstimatedFee:  &proto.Uint256{Data: []byte{0}},
		}, nil

	} else if curId.IsErc20() {

		erc20Abi, err := erc20.Erc20MetaData.GetAbi()
		if err != nil {
			return nil, err
		}

		addr, err := srv.Extensions.AddressFromString(req.To)
		if err != nil {
			return nil, err
		}

		data, err := erc20Abi.Pack("transfer", addr, big.NewInt(0).SetBytes(req.Amount.Value.Data))
		if err != nil {
			return nil, err
		}

		res, err := srv.client.TriggerSmartContract(ctx, TriggerSmartContractRequest{
			OwnerAddress:    req.From,
			ContractAddress: curId.Address,
			FeeLimit:        ERC20_FEE_LIMIT,
			CallValue:       0,
			Data:            common.Bytes2Hex(data),
			Visible:         true,
		})

		if err != nil {
			return nil, err
		}

		if !res.Result.Result {
			return nil, fmt.Errorf("failed to create tx: %s %s", res.Result.Code, res.Result.Message)
		}

		return &services.TransactionIntent{
			Id:            common.Hex2Bytes(res.Transaction.TxId),
			SignatureType: trx.Instance.SignatureType,
			PayloadToSign: common.Hex2Bytes(res.Transaction.TxId),
			RawData:       res.Transaction.RawData,
			EstimatedFee:  &proto.Uint256{Data: []byte{0}},
		}, nil

	} else {
		return nil, rpcerrors.ErrInvalidCurrency
	}
}

func (srv *TrxAgent) Send(ctx context.Context, req *services.TransactionSendRequest) (*services.TransactionSendResponse, error) {
	srv.Log.Debug("Send", "req", req)
	if srv.client == nil {
		return nil, errors.ErrUnsupported
	}

	var signatures []string
	for _, signature := range req.Signatures {
		signatures = append(signatures, common.Bytes2Hex(signature))
	}
	res, err := srv.client.BroadcastTransaction(ctx, BroadcastTransactionRequest{
		Visible:   true,
		RawData:   req.Intent.RawData,
		Signature: signatures,
	})

	if err != nil {
		return nil, err
	}

	if !res.Result {
		return nil, fmt.Errorf("failed to broadcast tx: %s %s", res.Code, res.Message)
	}

	return &services.TransactionSendResponse{
		Id: req.Intent.Id,
	}, nil
}
