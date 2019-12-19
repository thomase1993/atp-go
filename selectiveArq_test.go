package atp

import (
	"github.com/stretchr/testify/suite"
	"testing"
	"time"
)

type SelectiveArqTestSuite struct {
	atpTestSuite
	alphaArq, betaArq                 *selectiveArq
	alphaManipulator, betaManipulator *segmentManipulator
}

func newMockSelectiveArqConnection(connector *channelConnector, name string) (*selectiveArq, *segmentManipulator) {
	arq := newSelectiveArq(1, nil, testErrorChannel)
	printer := &consolePrinter{Name: name}
	manipulator := &segmentManipulator{}

	arq.addExtension(manipulator)
	manipulator.AddExtension(printer)
	printer.AddExtension(connector)

	return arq, manipulator
}

func (suite *SelectiveArqTestSuite) SetupTest() {
	endpoint1, endpoint2 := make(chan []byte, 100), make(chan []byte, 100)
	connector1, connector2 := &channelConnector{
		in:  endpoint1,
		out: endpoint2,
	}, &channelConnector{
		in:  endpoint2,
		out: endpoint1,
	}
	suite.alphaArq, suite.alphaManipulator = newMockSelectiveArqConnection(connector1, "alpha")
	suite.betaArq, suite.betaManipulator = newMockSelectiveArqConnection(connector2, "beta")
	segmentMtu = headerLength + 8
}

func (suite *SelectiveArqTestSuite) TearDownTest() {
	segmentMtu = defaultMTU
	suite.handleTestError(suite.alphaArq.Close())
	suite.handleTestError(suite.betaArq.Close())
}

func (suite *SelectiveArqTestSuite) TestQueueTimedOutSegmentsForWrite() {
	currentTime := time.Now()

	suite.alphaArq.notAckedSegment = append(suite.alphaArq.notAckedSegment, &segment{timestamp: currentTime.Add(-1), sequenceNumber: []byte{0, 0, 0, 1}})
	suite.alphaArq.notAckedSegment = append(suite.alphaArq.notAckedSegment, &segment{timestamp: currentTime.Add(-1), sequenceNumber: []byte{0, 0, 0, 2}})
	suite.alphaArq.notAckedSegment = append(suite.alphaArq.notAckedSegment, &segment{timestamp: currentTime.Add(-1), sequenceNumber: []byte{0, 0, 0, 3}})
	suite.alphaArq.notAckedSegment = append(suite.alphaArq.notAckedSegment, &segment{timestamp: currentTime.Add(20000), sequenceNumber: []byte{0, 0, 0, 4}})
	suite.alphaArq.queueTimedOutSegmentsForWrite(currentTime)

	i := uint32(1)
	for !suite.alphaArq.readyToSendSegmentQueue.IsEmpty() {
		ele := suite.alphaArq.readyToSendSegmentQueue.Dequeue()
		suite.Equal(i, ele.(*segment).getSequenceNumber())
		suite.NotEqual(4, ele.(*segment).getSequenceNumber())
		i++
	}
}

func (suite *SelectiveArqTestSuite) TestWriteQueuedSegments() {
	currentTime := time.Now()

	seg1 := createFlaggedSegment(1, 0, []byte("test"))
	seg1.timestamp = currentTime.Add(-1)
	seg2 := createFlaggedSegment(2, 0, []byte("test"))
	seg2.timestamp = currentTime.Add(-1)
	seg3 := createFlaggedSegment(3, 0, []byte("test"))
	seg3.timestamp = currentTime.Add(-1)
	seg4 := createFlaggedSegment(4, 0, []byte("test"))
	seg4.timestamp = currentTime.Add(20000)

	suite.alphaArq.readyToSendSegmentQueue.Enqueue(seg1)
	suite.alphaArq.readyToSendSegmentQueue.Enqueue(seg2)
	suite.alphaArq.readyToSendSegmentQueue.Enqueue(seg3)
	suite.alphaArq.readyToSendSegmentQueue.Enqueue(seg4)

	_, _, err := suite.alphaArq.writeQueuedSegments(currentTime)

	suite.True(suite.alphaArq.readyToSendSegmentQueue.IsEmpty())
	suite.Nil(err)
}

func (suite *SelectiveArqTestSuite) TestFullWindowFlag() {
	suite.alphaArq.windowSize = 4

	message := "AAAABBBBCCCCDDDDEEEEFFFFGGGGHHHHIIII"
	suite.writeExpectStatus(suite.alphaArq, message, windowFull, time.Now())
}

func (suite *SelectiveArqTestSuite) TestSendingACKs() {
	suite.alphaArq.windowSize = 8
	suite.betaArq.windowSize = 8

	message := "ABCDEFGHIJKLMNOPQRSTUVWXYZ123456"
	timestamp := time.Now()

	suite.write(suite.alphaArq, message, timestamp)

	suite.read(suite.betaArq, message[:8], timestamp)
	suite.read(suite.betaArq, message[8:16], timestamp)
	suite.read(suite.betaArq, message[16:24], timestamp)
	suite.read(suite.betaArq, message[24:32], timestamp)

	suite.readAck(suite.alphaArq, timestamp)
	suite.readAck(suite.alphaArq, timestamp)
	suite.readAck(suite.alphaArq, timestamp)
	suite.readAck(suite.alphaArq, timestamp)
}

func (suite *SelectiveArqTestSuite) TestRetransmissionAfterSegmentLoss() {
	suite.alphaArq.windowSize = 8
	suite.betaArq.windowSize = 8

	suite.alphaManipulator.DropOnce(2)
	suite.alphaManipulator.DropOnce(3)

	message := "ABCD1234EFGH5678IJKL9012MNOP3456"
	timestamp := time.Now()

	suite.write(suite.alphaArq, message, timestamp)

	suite.read(suite.betaArq, message[:8], timestamp)
	suite.readAck(suite.alphaArq, timestamp)

	suite.readExpectStatus(suite.betaArq, invalidSegment, timestamp)
	suite.readAck(suite.alphaArq, timestamp)

	suite.read(suite.betaArq, message[8:16], timestamp)
	suite.readAck(suite.alphaArq, timestamp)
	suite.read(suite.betaArq, message[16:24], timestamp)
	suite.readAck(suite.alphaArq, timestamp)
	suite.read(suite.betaArq, message[24:32], timestamp)

	suite.readExpectStatus(suite.betaArq, invalidSegment, timestamp)
	suite.readAck(suite.alphaArq, timestamp)
}

func (suite *SelectiveArqTestSuite) TestRetransmissionAfterLastSegmentTimeout() {
	suite.alphaArq.windowSize = 8
	suite.betaArq.windowSize = 8

	suite.alphaManipulator.DropOnce(3)

	message := "ABCD1234EFGH5678IJKL9012"

	timestamp := time.Now()

	suite.write(suite.alphaArq, message, timestamp)
	suite.read(suite.betaArq, message[:8], timestamp)
	suite.readAck(suite.alphaArq, timestamp)
	suite.read(suite.betaArq, message[8:16], timestamp)
	suite.readAck(suite.alphaArq, timestamp)
	timestamp = timestamp.Add(retransmissionTimeout + time.Millisecond)
	suite.write(suite.alphaArq, "", timestamp)
	suite.read(suite.betaArq, message[16:24], timestamp)
	suite.readAck(suite.alphaArq, timestamp)
}

func (suite *SelectiveArqTestSuite) TestRetransmissionAfterLostAck() {
	suite.alphaArq.windowSize = 8
	suite.betaArq.windowSize = 8

	suite.betaManipulator.DropOnce(3)

	message := "ABCD1234EFGH5678IJKL9012"

	timestamp := time.Now()

	suite.write(suite.alphaArq, message, timestamp)
	suite.read(suite.betaArq, message[:8], timestamp)
	suite.readAck(suite.alphaArq, timestamp)
	suite.read(suite.betaArq, message[8:16], timestamp)
	suite.readAck(suite.alphaArq, timestamp)
	suite.read(suite.betaArq, message[16:24], timestamp)
	timestamp = timestamp.Add(retransmissionTimeout + time.Millisecond)
	suite.write(suite.alphaArq, "", timestamp)
	suite.readExpectStatus(suite.betaArq, invalidSegment, timestamp)
	suite.readAck(suite.alphaArq, timestamp)
}

func TestSelectiveArq(t *testing.T) {
	suite.Run(t, new(SelectiveArqTestSuite))
}