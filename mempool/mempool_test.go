package mempool_test

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/cometbft/cometbft/libs/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/skip-mev/pob/mempool"
	testutils "github.com/skip-mev/pob/testutils"
	buildertypes "github.com/skip-mev/pob/x/builder/types"
	"github.com/stretchr/testify/suite"
)

type IntegrationTestSuite struct {
	suite.Suite

	encCfg   testutils.EncodingConfig
	config   mempool.AuctionFactory
	mempool  *mempool.AuctionMempool
	ctx      sdk.Context
	random   *rand.Rand
	accounts []testutils.Account
	nonces   map[string]uint64
}

func TestMempoolTestSuite(t *testing.T) {
	suite.Run(t, new(IntegrationTestSuite))
}

func (suite *IntegrationTestSuite) SetupTest() {
	// Mempool setup
	suite.encCfg = testutils.CreateTestEncodingConfig()
	suite.config = mempool.NewDefaultAuctionFactory(suite.encCfg.TxConfig.TxDecoder())
	suite.mempool = mempool.NewAuctionMempool(suite.encCfg.TxConfig.TxDecoder(), suite.encCfg.TxConfig.TxEncoder(), 0, suite.config)
	suite.ctx = sdk.NewContext(nil, cmtproto.Header{}, false, log.NewNopLogger())

	// Init accounts
	suite.random = rand.New(rand.NewSource(time.Now().Unix()))
	suite.accounts = testutils.RandomAccounts(suite.random, 10)

	suite.nonces = make(map[string]uint64)
	for _, acc := range suite.accounts {
		suite.nonces[acc.Address.String()] = 0
	}
}

// CreateFilledMempool creates a pre-filled mempool with numNormalTxs normal transactions, numAuctionTxs auction transactions, and numBundledTxs bundled
// transactions per auction transaction. If insertRefTxs is true, it will also insert a the referenced transactions into the mempool. This returns
// the total number of transactions inserted into the mempool.
func (suite *IntegrationTestSuite) CreateFilledMempool(numNormalTxs, numAuctionTxs, numBundledTxs int, insertRefTxs bool) int {
	// Insert a bunch of normal transactions into the global mempool
	for i := 0; i < numNormalTxs; i++ {
		// create a few random msgs

		// randomly select an account to create the tx
		randomIndex := suite.random.Intn(len(suite.accounts))
		acc := suite.accounts[randomIndex]
		nonce := suite.nonces[acc.Address.String()]
		randomMsgs := testutils.CreateRandomMsgs(acc.Address, 3)
		randomTx, err := testutils.CreateTx(suite.encCfg.TxConfig, acc, nonce, 100, randomMsgs)
		suite.Require().NoError(err)

		suite.nonces[acc.Address.String()]++
		priority := suite.random.Int63n(100) + 1
		suite.Require().NoError(suite.mempool.Insert(suite.ctx.WithPriority(priority), randomTx))
		contains, err := suite.mempool.Contains(randomTx)
		suite.Require().NoError(err)
		suite.Require().True(contains)
	}

	suite.Require().Equal(numNormalTxs, suite.mempool.CountTx())
	suite.Require().Equal(0, suite.mempool.CountAuctionTx())

	// Insert a bunch of auction transactions into the global mempool and auction mempool
	for i := 0; i < numAuctionTxs; i++ {
		// randomly select a bidder to create the tx
		acc := testutils.RandomAccounts(suite.random, 1)[0]

		// create a new auction bid msg with numBundledTxs bundled transactions
		priority := suite.random.Int63n(100) + 1
		bid := sdk.NewInt64Coin("foo", priority)
		nonce := suite.nonces[acc.Address.String()]
		bidMsg, err := testutils.CreateMsgAuctionBid(suite.encCfg.TxConfig, acc, bid, nonce, numBundledTxs)
		suite.nonces[acc.Address.String()] += uint64(numBundledTxs)
		suite.Require().NoError(err)

		// create the auction tx
		nonce = suite.nonces[acc.Address.String()]
		auctionTx, err := testutils.CreateTx(suite.encCfg.TxConfig, acc, nonce, 1000, []sdk.Msg{bidMsg})
		suite.Require().NoError(err)

		// insert the auction tx into the global mempool
		suite.Require().NoError(suite.mempool.Insert(suite.ctx.WithPriority(priority), auctionTx))
		contains, err := suite.mempool.Contains(auctionTx)
		suite.Require().NoError(err)
		suite.Require().True(contains)
		suite.nonces[acc.Address.String()]++

		if insertRefTxs {
			for _, refRawTx := range bidMsg.GetTransactions() {
				refTx, err := suite.encCfg.TxConfig.TxDecoder()(refRawTx)
				suite.Require().NoError(err)
				suite.Require().NoError(suite.mempool.Insert(suite.ctx.WithPriority(priority), refTx))
				contains, err = suite.mempool.Contains(refTx)
				suite.Require().NoError(err)
				suite.Require().True(contains)
			}
		}
	}

	var totalNumTxs int
	suite.Require().Equal(numAuctionTxs, suite.mempool.CountAuctionTx())
	if insertRefTxs {
		totalNumTxs = numNormalTxs + numAuctionTxs*(numBundledTxs)
		suite.Require().Equal(totalNumTxs, suite.mempool.CountTx())
	} else {
		suite.Require().Equal(totalNumTxs, suite.mempool.CountTx())
	}

	return totalNumTxs
}

func (suite *IntegrationTestSuite) TestAuctionMempoolRemove() {
	numberTotalTxs := 100
	numberAuctionTxs := 10
	numberBundledTxs := 5
	insertRefTxs := true
	numMempoolTxs := suite.CreateFilledMempool(numberTotalTxs, numberAuctionTxs, numberBundledTxs, insertRefTxs)

	// Select the top bid tx from the auction mempool and do sanity checks
	auctionIterator := suite.mempool.AuctionBidSelect(suite.ctx)
	suite.Require().NotNil(auctionIterator)
	tx := auctionIterator.Tx()
	suite.Require().Len(tx.GetMsgs(), 1)
	suite.Require().NoError(suite.mempool.Remove(tx))

	// Ensure that the auction tx was removed from the auction mempool only
	suite.Require().Equal(numberAuctionTxs-1, suite.mempool.CountAuctionTx())
	suite.Require().Equal(numMempoolTxs, suite.mempool.CountTx())
	contains, err := suite.mempool.Contains(tx)
	suite.Require().NoError(err)
	suite.Require().False(contains)

	// Attempt to remove again and ensure that the tx is not found
	suite.Require().NoError(suite.mempool.Remove(tx))
	suite.Require().Equal(numberAuctionTxs-1, suite.mempool.CountAuctionTx())
	suite.Require().Equal(numMempoolTxs, suite.mempool.CountTx())

	// Bundled txs should be in the global mempool
	auctionMsg, err := mempool.GetMsgAuctionBidFromTx(tx)
	suite.Require().NoError(err)
	for _, refTx := range auctionMsg.GetTransactions() {
		tx, err := suite.encCfg.TxConfig.TxDecoder()(refTx)
		suite.Require().NoError(err)
		contains, err = suite.mempool.Contains(tx)
		suite.Require().NoError(err)
		suite.Require().True(contains)
	}

	// Attempt to remove a global tx
	iterator := suite.mempool.Select(context.Background(), nil)
	tx = iterator.Tx()
	size := suite.mempool.CountTx()
	suite.mempool.Remove(tx)
	suite.Require().Equal(size-1, suite.mempool.CountTx())

	// Remove the rest of the global transactions
	iterator = suite.mempool.Select(context.Background(), nil)
	suite.Require().NotNil(iterator)
	for iterator != nil {
		tx = iterator.Tx()
		suite.Require().NoError(suite.mempool.Remove(tx))
		iterator = suite.mempool.Select(context.Background(), nil)
	}
	suite.Require().Equal(0, suite.mempool.CountTx())

	// Remove the rest of the auction transactions
	auctionIterator = suite.mempool.AuctionBidSelect(suite.ctx)
	for auctionIterator != nil {
		tx = auctionIterator.Tx()
		suite.Require().NoError(suite.mempool.Remove(tx))
		auctionIterator = suite.mempool.AuctionBidSelect(suite.ctx)
	}
	suite.Require().Equal(0, suite.mempool.CountAuctionTx())

	// Ensure that the mempool is empty
	iterator = suite.mempool.Select(context.Background(), nil)
	suite.Require().Nil(iterator)
	auctionIterator = suite.mempool.AuctionBidSelect(suite.ctx)
	suite.Require().Nil(auctionIterator)
	suite.Require().Equal(0, suite.mempool.CountTx())
	suite.Require().Equal(0, suite.mempool.CountAuctionTx())
}

func (suite *IntegrationTestSuite) TestAuctionMempoolSelect() {
	numberTotalTxs := 100
	numberAuctionTxs := 10
	numberBundledTxs := 5
	insertRefTxs := true
	totalTxs := suite.CreateFilledMempool(numberTotalTxs, numberAuctionTxs, numberBundledTxs, insertRefTxs)

	// iterate through the entire auction mempool and ensure the bids are in order
	var highestBid sdk.Coin
	var prevBid sdk.Coin
	auctionIterator := suite.mempool.AuctionBidSelect(suite.ctx)
	numberTxsSeen := 0
	for auctionIterator != nil {
		tx := auctionIterator.Tx()
		suite.Require().Len(tx.GetMsgs(), 1)

		msgAuctionBid := tx.GetMsgs()[0].(*buildertypes.MsgAuctionBid)
		if highestBid.IsNil() {
			highestBid = msgAuctionBid.Bid
			prevBid = msgAuctionBid.Bid
		} else {
			suite.Require().True(msgAuctionBid.Bid.IsLTE(highestBid))
			suite.Require().True(msgAuctionBid.Bid.IsLTE(prevBid))
			prevBid = msgAuctionBid.Bid
		}

		suite.Require().Len(msgAuctionBid.GetTransactions(), numberBundledTxs)

		auctionIterator = auctionIterator.Next()
		numberTxsSeen++
	}

	suite.Require().Equal(numberAuctionTxs, numberTxsSeen)

	iterator := suite.mempool.Select(context.Background(), nil)
	numberTxsSeen = 0
	for iterator != nil {
		iterator = iterator.Next()
		numberTxsSeen++
	}
	suite.Require().Equal(totalTxs, numberTxsSeen)
}
