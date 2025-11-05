package bfcp

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Message represents a BFCP protocol message with common header and attributes
type Message struct {
	// Common Header (12 bytes)
	Version       uint8     // 3 bits (always 2 for RFC 8855)
	Reserved      uint8     // 5 bits (must be 0)
	Primitive     Primitive // 8 bits
	PayloadLength uint16    // 16 bits (length in 4-byte words, excluding common header)
	ConferenceID  uint32    // 32 bits
	TransactionID uint16    // 16 bits
	UserID        uint16    // 16 bits

	// Attributes
	Attributes []Attribute
}

// Attribute represents a BFCP TLV attribute
type Attribute struct {
	Type   AttributeType
	Length uint8  // Length in bytes (not including Type and Length fields)
	Value  []byte // Actual attribute data
}

// NewMessage creates a new BFCP message with the given primitive
func NewMessage(primitive Primitive, conferenceID uint32, transactionID, userID uint16) *Message {
	return &Message{
		Version:       ProtocolVersion,
		Reserved:      0,
		Primitive:     primitive,
		ConferenceID:  conferenceID,
		TransactionID: transactionID,
		UserID:        userID,
		Attributes:    make([]Attribute, 0),
	}
}

// AddAttribute adds an attribute to the message
func (m *Message) AddAttribute(attrType AttributeType, value []byte) {
	m.Attributes = append(m.Attributes, Attribute{
		Type:   attrType,
		Length: uint8(len(value)),
		Value:  value,
	})
}

// AddFloorID adds a FLOOR-ID attribute
func (m *Message) AddFloorID(floorID uint16) {
	value := make([]byte, 2)
	binary.BigEndian.PutUint16(value, floorID)
	m.AddAttribute(AttrFloorID, value)
}

// AddFloorRequestID adds a FLOOR-REQUEST-ID attribute
func (m *Message) AddFloorRequestID(requestID uint16) {
	value := make([]byte, 2)
	binary.BigEndian.PutUint16(value, requestID)
	m.AddAttribute(AttrFloorRequestID, value)
}

// AddBeneficiaryID adds a BENEFICIARY-ID attribute
func (m *Message) AddBeneficiaryID(beneficiaryID uint16) {
	value := make([]byte, 2)
	binary.BigEndian.PutUint16(value, beneficiaryID)
	m.AddAttribute(AttrBeneficiaryID, value)
}

// AddRequestStatus adds a REQUEST-STATUS attribute
func (m *Message) AddRequestStatus(status RequestStatus, queuePosition uint8) {
	value := make([]byte, 2)
	value[0] = uint8(status)
	value[1] = queuePosition
	m.AddAttribute(AttrRequestStatus, value)
}

// AddErrorCode adds an ERROR-CODE attribute
func (m *Message) AddErrorCode(errorCode ErrorCode) {
	value := []byte{uint8(errorCode)}
	m.AddAttribute(AttrErrorCode, value)
}

// AddErrorInfo adds an ERROR-INFO attribute (text string)
func (m *Message) AddErrorInfo(info string) {
	m.AddAttribute(AttrErrorInfo, []byte(info))
}

// AddSupportedPrimitives adds a SUPPORTED-PRIMITIVES attribute
func (m *Message) AddSupportedPrimitives(primitives []Primitive) {
	value := make([]byte, len(primitives))
	for i, p := range primitives {
		value[i] = uint8(p)
	}
	m.AddAttribute(AttrSupportedPrimitives, value)
}

// AddSupportedAttributes adds a SUPPORTED-ATTRIBUTES attribute
func (m *Message) AddSupportedAttributes(attributes []AttributeType) {
	value := make([]byte, len(attributes))
	for i, a := range attributes {
		value[i] = uint8(a)
	}
	m.AddAttribute(AttrSupportedAttributes, value)
}

// AddPriority adds a PRIORITY attribute
func (m *Message) AddPriority(priority Priority) {
	value := make([]byte, 2)
	binary.BigEndian.PutUint16(value, uint16(priority))
	m.AddAttribute(AttrPriority, value)
}

// GetAttribute returns the first attribute of the given type, or nil if not found
func (m *Message) GetAttribute(attrType AttributeType) *Attribute {
	for i := range m.Attributes {
		if m.Attributes[i].Type == attrType {
			return &m.Attributes[i]
		}
	}
	return nil
}

// GetFloorID extracts the FloorID from FLOOR-ID attribute
func (m *Message) GetFloorID() (uint16, bool) {
	attr := m.GetAttribute(AttrFloorID)
	if attr == nil || len(attr.Value) < 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(attr.Value), true
}

// GetFloorRequestID extracts the FloorRequestID from FLOOR-REQUEST-ID attribute
func (m *Message) GetFloorRequestID() (uint16, bool) {
	attr := m.GetAttribute(AttrFloorRequestID)
	if attr == nil || len(attr.Value) < 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(attr.Value), true
}

// GetBeneficiaryID extracts the BeneficiaryID from BENEFICIARY-ID attribute
func (m *Message) GetBeneficiaryID() (uint16, bool) {
	attr := m.GetAttribute(AttrBeneficiaryID)
	if attr == nil || len(attr.Value) < 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(attr.Value), true
}

// GetRequestStatus extracts the RequestStatus from REQUEST-STATUS attribute
func (m *Message) GetRequestStatus() (RequestStatus, uint8, bool) {
	attr := m.GetAttribute(AttrRequestStatus)
	if attr == nil || len(attr.Value) < 2 {
		return 0, 0, false
	}
	return RequestStatus(attr.Value[0]), attr.Value[1], true
}

// GetErrorCode extracts the ErrorCode from ERROR-CODE attribute
func (m *Message) GetErrorCode() (ErrorCode, bool) {
	attr := m.GetAttribute(AttrErrorCode)
	if attr == nil || len(attr.Value) < 1 {
		return 0, false
	}
	return ErrorCode(attr.Value[0]), true
}

// GetErrorInfo extracts the error information string from ERROR-INFO attribute
func (m *Message) GetErrorInfo() (string, bool) {
	attr := m.GetAttribute(AttrErrorInfo)
	if attr == nil {
		return "", false
	}
	return string(attr.Value), true
}

// Encode serializes the BFCP message to bytes according to RFC 8855 Section 5
// Message format:
//  0                   1                   2                   3
//  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |Ver|  Resv |P|  Primitive |            Length                   |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |         ConferenceID          |        TransactionID          |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |           UserID              |       (Start of Attributes)   |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
func (m *Message) Encode() ([]byte, error) {
	// Calculate total attribute length
	attrLength := 0
	for _, attr := range m.Attributes {
		// Each attribute: Type(1) + Length(1) + Value(Length) + padding to 4-byte boundary
		attrLen := 2 + int(attr.Length)
		// Pad to 4-byte boundary
		if padding := attrLen % 4; padding != 0 {
			attrLen += 4 - padding
		}
		attrLength += attrLen
	}

	// PayloadLength is in 4-byte words (excluding common header)
	if attrLength%4 != 0 {
		return nil, fmt.Errorf("attribute length not aligned to 4-byte boundary")
	}
	m.PayloadLength = uint16(attrLength / 4)

	// Total message size
	totalSize := CommonHeaderLength + attrLength
	buf := make([]byte, totalSize)

	// Encode common header
	// Byte 0: Ver(3 bits) + Reserved(5 bits)
	buf[0] = (m.Version << 5) | (m.Reserved & 0x1F)

	// Byte 1: Primitive (8 bits)
	buf[1] = uint8(m.Primitive)

	// Bytes 2-3: PayloadLength (16 bits)
	binary.BigEndian.PutUint16(buf[2:4], m.PayloadLength)

	// Bytes 4-7: ConferenceID (32 bits)
	binary.BigEndian.PutUint32(buf[4:8], m.ConferenceID)

	// Bytes 8-9: TransactionID (16 bits)
	binary.BigEndian.PutUint16(buf[8:10], m.TransactionID)

	// Bytes 10-11: UserID (16 bits)
	binary.BigEndian.PutUint16(buf[10:12], m.UserID)

	// Encode attributes
	offset := CommonHeaderLength
	for _, attr := range m.Attributes {
		// Type (1 byte)
		buf[offset] = uint8(attr.Type)
		offset++

		// Length (1 byte)
		buf[offset] = attr.Length
		offset++

		// Value (Length bytes)
		copy(buf[offset:], attr.Value)
		offset += int(attr.Length)

		// Padding to 4-byte boundary
		if padding := (2 + int(attr.Length)) % 4; padding != 0 {
			offset += 4 - padding
		}
	}

	return buf, nil
}

// Decode parses a BFCP message from bytes according to RFC 8855 Section 5
func Decode(data []byte) (*Message, error) {
	if len(data) < CommonHeaderLength {
		return nil, fmt.Errorf("message too short: got %d bytes, need at least %d", len(data), CommonHeaderLength)
	}

	msg := &Message{}

	// Parse common header
	// Byte 0: Ver(3 bits) + Reserved(5 bits)
	msg.Version = (data[0] >> 5) & 0x07
	msg.Reserved = data[0] & 0x1F

	if msg.Version != ProtocolVersion {
		return nil, fmt.Errorf("unsupported BFCP version: %d (expected %d)", msg.Version, ProtocolVersion)
	}

	// Byte 1: Primitive
	msg.Primitive = Primitive(data[1])

	// Bytes 2-3: PayloadLength (in 4-byte words)
	msg.PayloadLength = binary.BigEndian.Uint16(data[2:4])

	// Bytes 4-7: ConferenceID
	msg.ConferenceID = binary.BigEndian.Uint32(data[4:8])

	// Bytes 8-9: TransactionID
	msg.TransactionID = binary.BigEndian.Uint16(data[8:10])

	// Bytes 10-11: UserID
	msg.UserID = binary.BigEndian.Uint16(data[10:12])

	// Calculate expected payload size
	expectedPayloadSize := int(msg.PayloadLength) * 4
	if len(data) < CommonHeaderLength+expectedPayloadSize {
		return nil, fmt.Errorf("message payload too short: got %d bytes, expected %d",
			len(data)-CommonHeaderLength, expectedPayloadSize)
	}

	// Parse attributes
	offset := CommonHeaderLength
	endOffset := CommonHeaderLength + expectedPayloadSize

	for offset < endOffset {
		if offset+2 > endOffset {
			return nil, fmt.Errorf("incomplete attribute header at offset %d", offset)
		}

		// Type (1 byte)
		attrType := AttributeType(data[offset])
		offset++

		// Length (1 byte)
		attrLength := data[offset]
		offset++

		// Value (attrLength bytes)
		if offset+int(attrLength) > endOffset {
			return nil, fmt.Errorf("attribute value exceeds message bounds at offset %d", offset)
		}

		value := make([]byte, attrLength)
		copy(value, data[offset:offset+int(attrLength)])
		offset += int(attrLength)

		msg.Attributes = append(msg.Attributes, Attribute{
			Type:   attrType,
			Length: attrLength,
			Value:  value,
		})

		// Skip padding to 4-byte boundary
		attrTotalLen := 2 + int(attrLength)
		if padding := attrTotalLen % 4; padding != 0 {
			offset += 4 - padding
		}
	}

	return msg, nil
}

// ReadMessage reads a single BFCP message from a reader (for TCP framing)
func ReadMessage(r io.Reader) (*Message, error) {
	// Read common header first
	header := make([]byte, CommonHeaderLength)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("failed to read message header: %w", err)
	}

	// Parse payload length from header
	payloadLength := binary.BigEndian.Uint16(header[2:4])
	payloadSize := int(payloadLength) * 4

	// Read the rest of the message
	fullMessage := make([]byte, CommonHeaderLength+payloadSize)
	copy(fullMessage, header)

	if payloadSize > 0 {
		if _, err := io.ReadFull(r, fullMessage[CommonHeaderLength:]); err != nil {
			return nil, fmt.Errorf("failed to read message payload: %w", err)
		}
	}

	// Decode the full message
	return Decode(fullMessage)
}

// WriteMessage writes a BFCP message to a writer (for TCP framing)
func WriteMessage(w io.Writer, msg *Message) error {
	data, err := msg.Encode()
	if err != nil {
		return fmt.Errorf("failed to encode message: %w", err)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	return nil
}
