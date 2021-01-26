package rpc

import (
	"context"
	"errors"
	"math/big"
	"strconv"

	"github.com/xuperchain/xupercore/bcs/ledger/xledger/xldgpb"
	"github.com/xuperchain/xupercore/kernel/engines/xuperos/reader"
	"github.com/xuperchain/xupercore/kernel/network/p2p"
	"github.com/xuperchain/xupercore/protos"

	rctx "github.com/xuperchain/xuperos/common/context"
	"github.com/xuperchain/xuperos/common/xupospb/pb"
)

// 注意：
// 1.rpc接口响应resp不能为nil，必须实例化
// 2.rpc接口响应err必须为ecom.Error类型的标准错误，没有错误响应err=nil
// 3.rpc接口不需要关注resp.Header，由拦截器根据err统一设置
// 4.rpc接口可以调用log库提供的SetInfoField方法附加输出到ending log

// PostTx post transaction to blockchain network
func (t *RpcServ) PostTx(gctx context.Context, req *pb.TxStatus) (*pb.CommonReply, error) {
	// 默认响应
	resp := &pb.CommonReply{}
	// 获取请求上下文，对内传递rctx
	rctx := sctx.ValueReqCtx(gctx)

	// 校验参数
	if req == nil || req.GetTx() == nil || req.GetBcname() == "" {
		rctx.GetLog().Warn("param error,some param unset")
		return resp, ecom.ErrParameter
	}
	tx := TxToXledger(req.GetTx())
	if tx == nil {
		rctx.GetLog().Warn("param error,tx convert to xledger tx failed")
		return resp, ecom.ErrParameter
	}

	// 提交交易
	handle, err := models.NewChainHandle(req.GetBcname(), rctx)
	if err != nil {
		rctx.GetLog().Warn("new chain handle failed", "err", err.Error())
		return resp, err
	}
	err = handle.SubmitTx(req.GetTx())
	rctx.GetLog().SetInfoField("bc_name", req.GetBcname())
	rctx.GetLog().SetInfoField("txid", utils.F(req.GetTxid()))
	return resp, err
}

// PreExec smart contract preExec process
func (t *RpcServ) PreExec(gctx context.Context, req *pb.InvokeRPCRequest) (*pb.InvokeRPCResponse, error) {
	// 默认响应
	resp := &pb.InvokeRPCResponse{}
	// 获取请求上下文，对内传递rctx
	rctx := sctx.ValueReqCtx(gctx)

	// 校验参数
	if req == nil || req.GetBcname() == "" || len(req.GetRequests()) < 1 {
		rctx.GetLog().Warn("param error,some param unset")
		return resp, ecom.ErrParameter
	}
	reqs, err := ConvertInvokeReq(req.GetRequests())
	if err != nil {
		rctx.GetLog().Warn("param error, convert failed", "err", err)
		return resp, ecom.ErrParameter
	}

	// 预执行
	handle, err := models.NewChainHandle(req.GetBcname(), rctx)
	if err != nil {
		rctx.GetLog().Warn("new chain handle failed", "err", err.Error())
		return resp, err
	}
	res, err := handle.PreExec(reqs, req.GetInitiator(), req.GetAuthRequire())
	rctx.GetLog().SetInfoField("bc_name", req.GetBcname())
	rctx.GetLog().SetInfoField("initiator", req.GetInitiator())
	// 设置响应
	if err == nil {
		resp.Bcname = req.GetBcname()
		resp.Response = ConvertInvokeResp(res)
	}

	return resp, err
}

// PreExecWithSelectUTXO preExec + selectUtxo
func (t *RpcServ) PreExecWithSelectUTXO(gctx context.Context,
	req *pb.PreExecWithSelectUTXORequest) (*pb.PreExecWithSelectUTXOResponse, error) {

	// 默认响应
	resp := &pb.PreExecWithSelectUTXOResponse{}
	// 获取请求上下文，对内传递rctx
	rctx := sctx.ValueReqCtx(gctx)

	if req == nil || req.GetBcname() == "" || len(req.GetRequest()) < 1 {
		rctx.GetLog().Warn("param error,some param unset")
		return resp, ecom.ErrParameter
	}

	// PreExec
	preExecRes, err := t.PreExec(gctx, req.GetRequest())
	if err != nil {
		rctx.GetLog().Warn("pre exec failed", "err", err)
		return resp, err
	}

	// SelectUTXO
	totalAmount := req.GetTotalAmount() + preExecRes.GetResponse().GetGasUsed()
	if totalAmount < 1 {
		return resp, nil
	}
	utxoInput := &pb.UtxoInput{
		Header:    req.GetHeader(),
		Bcname:    req.GetBcname(),
		Address:   req.GetAddress(),
		Publickey: req.GetSignInfo().GetPublicKey(),
		TotalNeed: big.NewInt(totalAmount).String(),
		UserSign:  req.GetSignInfo().GetSign(),
		NeedLock:  req.GetNeedLock(),
	}
	utxoOut, err := t.SelectUTXO(gctx, utxoInput)
	if err != nil {
		return resp, err
	}
	utxoOut.Header = req.GetHeader()

	// 设置响应
	resp.Bcname = req.GetBcname()
	resp.Response = preExecRes.GetResponse()
	resp.UtxoOutput = utxoOut

	return resp, nil
}

// SelectUTXO select utxo inputs depending on amount
func (t *RpcServ) SelectUTXO(gctx context.Context, req *pb.UtxoInput) (*pb.UtxoOutput, error) {
	// 默认响应
	resp := &pb.UtxoOutput{}
	// 获取请求上下文，对内传递rctx
	rctx := sctx.ValueReqCtx(gctx)

	if req == nil || req.GetBcname() == "" || req.GetTotalNeed() == "" {
		rctx.GetLog().Warn("param error,some param unset")
		return resp, ecom.ErrParameter
	}
	totalNeed, ok := new(big.Int).SetString(req.GetTotalNeed(), 10)
	if !ok {
		rctx.GetLog().Warn("param error,total need set error", "totalNeed", req.GetTotalNeed())
		return resp, ecom.ErrParameter
	}

	// select utxo
	handle, err := models.NewChainHandle(req.GetBcname(), rctx)
	if err != nil {
		rctx.GetLog().Warn("new chain handle failed", "err", err.Error())
		return resp, err
	}
	out, err := handle.SelectUtxo(req.GetAddress(), totalNeed, req.GetNeedLock(), false,
		req.GetPublickey(), req.GetUserSign())
	if err != nil {
		rctx.GetLog().Warn("select utxo failed", "err", err.Error())
		return resp, err
	}

	utxoList, err := UtxoListToXchain(out.GetUtxoList())
	if err != nil {
		rctx.GetLog().Warn("convert utxo failed", "err", err)
		return resp, ecom.ErrInternal
	}

	resp.UtxoList = utxoList
	resp.TotalSelected = out.GetTotalSelected()
	return resp, nil
}

// SelectUTXOBySize select utxo inputs depending on size
func (s *RpcServ) SelectUTXOBySize(ctx context.Context, in *pb.UtxoInput) (*pb.UtxoOutput, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.UtxoResponse{Header: defRespHeader(in.Header)}

	utxoReader := reader.NewUtxoReader(chain.Context(), reqCtx)
	response, err := utxoReader.SelectUTXOBySize(in.GetAddress(), in.GetNeedLock(), false)
	if err != nil {
		out.Header.Error = ErrorEnum(err)
		reqCtx.GetLog().Warn("failed to select utxo", "error", err)
		return out, err
	}

	out.UtxoList = response.UtxoList
	out.TotalSelected = response.TotalSelected
	reqCtx.GetLog().SetInfoField("totalSelect", out.TotalSelected)
	return out, nil
}

// QueryContractStatData query statistic info about contract
func (s *RpcServ) QueryContractStatData(ctx context.Context, in *pb.ContractStatDataRequest) (*pb.ContractStatDataResponse, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.ContractStatDataResponse{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	contractReader := reader.NewContractReader(chain.Context(), reqCtx)
	contractStatData, err := contractReader.QueryContractStatData()
	if err != nil {
		return nil, err
	}

	out.Data = contractStatData
	return out, nil
}

// QueryUtxoRecord query utxo records
func (s *RpcServ) QueryUtxoRecord(ctx context.Context, in *pb.UtxoRecordDetails) (*pb.UtxoRecordDetails, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.UtxoRecordDetails{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	utxoReader := reader.NewUtxoReader(chain.Context(), reqCtx)
	if len(in.GetAccountName()) > 0 {
		utxoRecord, err := utxoReader.QueryUtxoRecord(in.GetAccountName(), in.GetDisplayCount())
		if err != nil {
			reqCtx.GetLog().Warn("query utxo record error", "account", in.GetAccountName())
			return out, err
		}

		out.FrozenUtxoRecord = utxoRecord.FrozenUtxo
		out.LockedUtxoRecord = utxoRecord.LockedUtxo
		out.OpenUtxoRecord = utxoRecord.OpenUtxo
		return out, nil
	}

	return out, nil
}

// QueryACL query some account info
func (s *RpcServ) QueryACL(ctx context.Context, in *pb.AclStatus) (*pb.AclStatus, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.AclStatus{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	contractReader := reader.NewContractReader(chain.Context(), reqCtx)
	accountName := in.GetAccountName()
	contractName := in.GetContractName()
	methodName := in.GetMethodName()
	if len(accountName) > 0 {
		acl, err := contractReader.QueryAccountACL(accountName)
		if err != nil {
			out.Confirmed = false
			reqCtx.GetLog().Warn("query account acl error", "account", accountName)
			return out, err
		}
		out.Confirmed = true
		out.Acl = acl
	} else if len(contractName) > 0 {
		if len(methodName) > 0 {
			acl, err := contractReader.QueryContractMethodACL(contractName, methodName)
			if err != nil {
				out.Confirmed = false
				reqCtx.GetLog().Warn("query contract method acl error", "account", accountName, "method", methodName)
				return out, err
			}
			out.Confirmed = true
			out.Acl = acl
		}
	}
	return out, nil
}

// GetAccountContracts get account request
func (s *RpcServ) GetAccountContracts(ctx context.Context, in *pb.GetAccountContractsRequest) (*pb.GetAccountContractsResponse, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.GetAccountContractsResponse{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	contractReader := reader.NewContractReader(chain.Context(), reqCtx)
	contractsStatus, err := contractReader.GetAccountContracts(in.GetAccount())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_ACCOUNT_CONTRACT_STATUS_ERROR
		reqCtx.GetLog().Warn("GetAccountContracts error", "error", err)
		return out, err
	}
	out.ContractsStatus = contractsStatus
	return out, nil
}

// QueryTx Get transaction details
func (s *RpcServ) QueryTx(ctx context.Context, in *pb.TxStatus) (*pb.TxStatus, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.TxStatus{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	ledgerReader := reader.NewLedgerReader(chain.Context(), reqCtx)
	txInfo, err := ledgerReader.QueryTx(in.GetTxid())
	if err != nil {
		reqCtx.GetLog().Warn("query tx error", "txid", in.GetTxid())
		return out, err
	}

	out.Tx = txInfo.Tx
	out.Status = txInfo.Status
	out.Distance = txInfo.Distance
	return out, nil
}

// GetBalance get balance for account or addr
func (s *RpcServ) GetBalance(ctx context.Context, in *pb.AddressStatus) (*pb.AddressStatus, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)

	for i := 0; i < len(in.Bcs); i++ {
		chain, err := s.engine.Get(in.Bcs[i].Bcname)
		if err != nil {
			in.Bcs[i].Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
			in.Bcs[i].Balance = ""
			continue
		}

		utxoReader := reader.NewUtxoReader(chain.Context(), reqCtx)
		balance, err := utxoReader.GetBalance(in.Address)
		if err != nil {
			in.Bcs[i].Error = ErrorEnum(err)
			in.Bcs[i].Balance = ""
		} else {
			in.Bcs[i].Error = pb.XChainErrorEnum_SUCCESS
			in.Bcs[i].Balance = balance
		}
	}
	return in, nil
}

// GetFrozenBalance get balance frozened for account or addr
func (s *RpcServ) GetFrozenBalance(ctx context.Context, in *pb.AddressStatus) (*pb.AddressStatus, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)

	for i := 0; i < len(in.Bcs); i++ {
		chain, err := s.engine.Get(in.Bcs[i].Bcname)
		if err != nil {
			in.Bcs[i].Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
			in.Bcs[i].Balance = ""
			continue
		}

		utxoReader := reader.NewUtxoReader(chain.Context(), reqCtx)
		balance, err := utxoReader.GetFrozenBalance(in.Address)
		if err != nil {
			in.Bcs[i].Error = ErrorEnum(err)
			in.Bcs[i].Balance = ""
		} else {
			in.Bcs[i].Error = pb.XChainErrorEnum_SUCCESS
			in.Bcs[i].Balance = balance
		}
	}

	return in, nil
}

// GetBalanceDetail get balance frozened for account or addr
func (s *RpcServ) GetBalanceDetail(ctx context.Context, in *pb.AddressBalanceStatus) (*pb.AddressBalanceStatus, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)

	for i := 0; i < len(in.Tfds); i++ {
		chain, err := s.engine.Get(in.Tfds[i].Bcname)
		if err != nil {
			in.Tfds[i].Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
			in.Tfds[i].Tfd = nil
			continue
		}

		utxoReader := reader.NewUtxoReader(chain.Context(), reqCtx)
		tfd, err := utxoReader.GetBalanceDetail(in.Address)
		if err != nil {
			in.Tfds[i].Error = ErrorEnum(err)
			in.Tfds[i].Tfd = nil
		} else {
			in.Tfds[i].Error = pb.XChainErrorEnum_SUCCESS
			// TODO: 使用了ledger定义的类型，验证是否有效
			in.Tfds[i].Tfd = tfd
		}
	}

	return in, nil
}

// GetBlock get block info according to blockID
func (s *RpcServ) GetBlock(ctx context.Context, in *pb.BlockID) (*pb.Block, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.Block{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	ledgerReader := reader.NewLedgerReader(chain.Context(), reqCtx)
	blockInfo, err := ledgerReader.QueryBlock(in.Blockid, true)
	if err != nil {
		reqCtx.GetLog().Warn("query block error", "error", err)
		return out, nil
	}

	// 类型转换：ledger.BlockInfo => pb.Block
	out.Block = blockInfo.Block
	out.Status = pb.Block_EBlockStatus(blockInfo.Status)

	block := blockInfo.GetBlock()
	transactions := block.GetTransactions()
	transactionsFilter := make([]*ledger.Transaction, 0, len(transactions))
	for _, transaction := range transactions {
		transactionsFilter = append(transactionsFilter, transaction)
	}

	if transactions != nil {
		out.Block.Transactions = transactionsFilter
	}

	reqCtx.GetLog().SetInfoField("blockid", out.GetBlockid())
	reqCtx.GetLog().SetInfoField("height", out.GetBlock().GetHeight())
	return out, nil
}

// GetBlockChainStatus get systemstatus
func (s *RpcServ) GetBlockChainStatus(ctx context.Context, in *pb.BCStatus) (*pb.BCStatus, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.BCStatus{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	chainReader := reader.NewChainReader(chain.Context(), reqCtx)
	status, err := chainReader.GetChainStatus()
	if err != nil {
		reqCtx.GetLog().Warn("get chain status error", "error", err)
	}

	// 类型转换：=> pb.BCStatus
	out.Meta = status.LedgerMeta
	out.Block = status.Block
	out.UtxoMeta = status.UtxoMeta
	return out, nil
}

// ConfirmBlockChainStatus confirm is_trunk
func (s *RpcServ) ConfirmBlockChainStatus(ctx context.Context, in *pb.BCStatus) (*pb.BCTipStatus, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.BCTipStatus{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	chainReader := reader.NewChainReader(chain.Context(), reqCtx)
	isTrunkTip, err := chainReader.IsTrunkTipBlock(in.GetBlock().GetBlockid())
	if err != nil {
		return nil, err
	}

	out.IsTrunkTip = isTrunkTip
	return out, nil
}

// GetBlockChains get BlockChains
func (s *RpcServ) GetBlockChains(ctx context.Context, in *pb.CommonIn) (*pb.BlockChains, error) {
	out := &pb.BlockChains{Header: defRespHeader(in.Header)}
	out.Blockchains = s.engine.GetChains()
	return out, nil
}

// GetSystemStatus get systemstatus
func (s *RpcServ) GetSystemStatus(ctx context.Context, in *pb.CommonIn) (*pb.SystemsStatusReply, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.SystemsStatusReply{Header: defRespHeader(in.Header)}

	systemsStatus := &pb.SystemsStatus{
		Header: in.Header,
		Speeds: &pb.Speeds{
			SumSpeeds: make(map[string]float64),
			BcSpeeds:  make(map[string]*pb.BCSpeeds),
		},
	}
	bcs := s.engine.GetChains()
	for _, bcName := range bcs {
		bcStatus := &pb.BCStatus{Header: in.Header, Bcname: bcName}
		status, err := s.GetBlockChainStatus(ctx, bcStatus)
		if err != nil {
			reqCtx.GetLog().Warn("get chain status error", "error", err)
		}

		systemsStatus.BcsStatus = append(systemsStatus.BcsStatus, status)
	}

	if in.ViewOption == pb.ViewOption_NONE || in.ViewOption == pb.ViewOption_PEERS {
		peerInfo := s.engine.Context().Net.PeerInfo()
		peerUrls := make([]string, 0, len(peerInfo.Peer))
		for _, peer := range peerInfo.Peer {
			peerUrls = append(peerUrls, peer.Address)
		}
		systemsStatus.PeerUrls = peerUrls
	}

	out.SystemsStatus = systemsStatus
	return out, nil
}

// GetNetURL get net url in p2p_base
func (s *RpcServ) GetNetURL(ctx context.Context, in *pb.CommonIn) (*pb.RawUrl, error) {
	out := &pb.RawUrl{Header: defRespHeader(in.Header)}
	peerInfo := s.engine.Context().Net.PeerInfo()
	out.RawUrl = peerInfo.Address
	return out, nil
}

// GetBlockByHeight  get trunk block by height
func (s *RpcServ) GetBlockByHeight(ctx context.Context, in *pb.BlockHeight) (*pb.Block, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.Block{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	ledgerReader := reader.NewLedgerReader(chain.Context(), reqCtx)
	blockInfo, err := ledgerReader.QueryBlockByHeight(in.Height, true)
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCK_EXIST_ERROR
		reqCtx.GetLog().Warn("query block error", "bc", in.GetBcname(), "height", in.Height)
		return out, err
	}

	out.Block = blockInfo.Block
	out.Status = pb.Block_EBlockStatus(blockInfo.Status)

	transactions := out.GetBlock().GetTransactions()
	if transactions != nil {
		out.Block.Transactions = transactions
	}

	reqCtx.GetLog().SetInfoField("height", in.Height)
	reqCtx.GetLog().SetInfoField("blockid", out.GetBlockid())
	return out, nil
}

// GetAccountByAK get account list with contain ak
func (s *RpcServ) GetAccountByAK(ctx context.Context, in *pb.AK2AccountRequest) (*pb.AK2AccountResponse, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.AK2AccountResponse{Header: defRespHeader(in.Header), Bcname: in.GetBcname()}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	contractReader := reader.NewContractReader(chain.Context(), reqCtx)
	accounts, err := contractReader.GetAccountByAK(in.GetAddress())
	if err != nil || accounts == nil {
		reqCtx.GetLog().Warn("QueryAccountContainAK error", "logid", out.Header.Logid, "error", err)
		return out, err
	}

	out.Account = accounts
	return out, err
}

// GetAddressContracts get contracts of accounts contain a specific address
func (s *RpcServ) GetAddressContracts(ctx context.Context, in *pb.AddressContractsRequest) (*pb.AddressContractsResponse, error) {
	reqCtx := rctx.ReqCtxFromContext(ctx)
	out := &pb.AddressContractsResponse{Header: defRespHeader(in.Header)}

	chain, err := s.engine.Get(in.GetBcname())
	if err != nil {
		out.Header.Error = pb.XChainErrorEnum_BLOCKCHAIN_NOTEXIST
		reqCtx.GetLog().Warn("block chain not exists", "bc", in.GetBcname())
		return out, err
	}

	contractReader := reader.NewContractReader(chain.Context(), reqCtx)
	accounts, err := contractReader.GetAccountByAK(in.GetAddress())
	if err != nil || accounts == nil {
		reqCtx.GetLog().Warn("QueryAccountContainAK error", "logid", out.Header.Logid, "error", err)
		return out, err
	}

	// get contracts for each account
	out.Contracts = make(map[string]*pb.ContractList)
	for _, account := range accounts {
		contracts, err := contractReader.GetAccountContracts(account)
		if err != nil {
			reqCtx.GetLog().Warn("GetAddressContracts partial account error", "logid", out.Header.Logid, "error", err)
			continue
		}

		if len(contracts) > 0 {
			out.Contracts[account] = &pb.ContractList{
				ContractStatus: contracts,
			}
		}
	}
	return out, nil
}

// DposCandidates get all candidates of the tdpos consensus
func (s *RpcServ) DposCandidates(context.Context, *pb.DposCandidatesRequest) (*pb.DposCandidatesResponse, error) {
	return nil, nil
}

// DposNominateRecords get all records nominated by an user
func (s *RpcServ) DposNominateRecords(context.Context, *pb.DposNominateRecordsRequest) (*pb.DposNominateRecordsResponse, error) {
	return nil, nil
}

// DposNomineeRecords get nominated record of a candidate
func (s *RpcServ) DposNomineeRecords(context.Context, *pb.DposNomineeRecordsRequest) (*pb.DposNomineeRecordsResponse, error) {
	return nil, nil
}

// DposVoteRecords get all vote records voted by an user
func (s *RpcServ) DposVoteRecords(context.Context, *pb.DposVoteRecordsRequest) (*pb.DposVoteRecordsResponse, error) {
	return nil, nil
}

// DposVotedRecords get all vote records of a candidate
func (s *RpcServ) DposVotedRecords(context.Context, *pb.DposVotedRecordsRequest) (*pb.DposVotedRecordsResponse, error) {
	return nil, nil
}

// DposCheckResults get check results of a specific term
func (s *RpcServ) DposCheckResults(context.Context, *pb.DposCheckResultsRequest) (*pb.DposCheckResultsResponse, error) {
	return nil, nil
}

// DposStatus get dpos status
func (s *RpcServ) DposStatus(context.Context, *pb.DposStatusRequest) (*pb.DposStatusResponse, error) {
	return nil, nil
}
