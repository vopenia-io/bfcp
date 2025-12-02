package bfcp

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
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

// AddFloorRequestInformation adds a FLOOR-REQUEST-INFORMATION grouped attribute
// containing the floor request ID, overall status, and floor-specific statuses.
// Per RFC 4582/8855, FloorRequestStatus MUST contain this grouped attribute.
//
// RFC 4582 FLOOR-REQUEST-INFORMATION format (Figure 23):
//
//	0                   1                   2                   3
//	0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |0 0 0 1 1 1 1|M|    Length     |       Floor Request ID        |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |                                                               |
// /                  (Nested attributes follow)                   /
//
// The Floor Request ID is part of the first 4-byte header word (bytes 2-3),
// NOT a separate field with padding. Nested attributes start immediately after.
func (m *Message) AddFloorRequestInformation(requestID uint16, status RequestStatus, queuePos uint8, floorIDs ...uint16) {
	// Build the grouped attribute content
	// NOTE: Floor Request ID is placed in bytes 2-3 of the attribute by Encode()
	// via special handling, so we only include nested attributes in content here.
	var content []byte

	// 1. Floor Request ID (16-bit) - goes in bytes 2-3 of the 4-byte header
	// This is handled specially - we put it first, no padding after it
	reqIDBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(reqIDBytes, requestID)
	content = append(content, reqIDBytes...)

	// 2. OVERALL-REQUEST-STATUS (type 18) containing REQUEST-STATUS
	// Starts immediately after Floor Request ID (no padding!)
	overallStatus := m.buildOverallRequestStatus(status, queuePos)
	content = append(content, overallStatus...)

	// 3. FLOOR-REQUEST-STATUS (type 17) for each floor
	for _, floorID := range floorIDs {
		floorStatus := m.buildFloorRequestStatus(floorID)
		content = append(content, floorStatus...)
	}

	m.AddAttribute(AttrFloorRequestInfo, content)
}

// buildOverallRequestStatus builds an OVERALL-REQUEST-STATUS sub-attribute (type 18)
// containing a nested REQUEST-STATUS attribute.
func (m *Message) buildOverallRequestStatus(status RequestStatus, queuePos uint8) []byte {
	// Nested REQUEST-STATUS attribute: type(1) + len(1) + value(2) = 4 bytes
	// For v1: length includes header (4), for v2: length is value only (2)
	statusLen := uint8(2)
	if m.Version == ProtocolVersionRFC4582 {
		statusLen = 4 // v1: includes Type+Length
	}
	statusAttr := []byte{
		uint8(AttrRequestStatus), // Type 5
		statusLen,                // Length
		uint8(status),            // Status
		queuePos,                 // Queue position
	}

	// OVERALL-REQUEST-STATUS: type(1) + len(1) + content(4)
	// For v1: length includes header (6), for v2: length is value only (4)
	overallLen := uint8(len(statusAttr))
	if m.Version == ProtocolVersionRFC4582 {
		overallLen = uint8(len(statusAttr) + 2) // v1: includes Type+Length
	}
	attr := []byte{
		uint8(AttrOverallRequestStatus), // Type 18
		overallLen,                       // Length
	}
	attr = append(attr, statusAttr...)

	// Pad to 4-byte boundary if needed
	if padding := len(attr) % 4; padding != 0 {
		attr = append(attr, make([]byte, 4-padding)...)
	}

	return attr
}

// buildFloorRequestStatus builds a FLOOR-REQUEST-STATUS sub-attribute (type 17)
// containing a nested FLOOR-ID attribute.
func (m *Message) buildFloorRequestStatus(floorID uint16) []byte {
	// FLOOR-ID value: 2 bytes
	floorIDBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(floorIDBytes, floorID)

	// Nested FLOOR-ID attribute: type(1) + len(1) + value(2) = 4 bytes
	// For v1: length includes header (4), for v2: length is value only (2)
	floorLen := uint8(2)
	if m.Version == ProtocolVersionRFC4582 {
		floorLen = 4 // v1: includes Type+Length
	}
	floorAttr := []byte{
		uint8(AttrFloorID), // Type 2
		floorLen,           // Length
	}
	floorAttr = append(floorAttr, floorIDBytes...)

	// FLOOR-REQUEST-STATUS: type(1) + len(1) + content(4)
	// For v1: length includes header (6), for v2: length is value only (4)
	statusLen := uint8(len(floorAttr))
	if m.Version == ProtocolVersionRFC4582 {
		statusLen = uint8(len(floorAttr) + 2) // v1: includes Type+Length
	}
	attr := []byte{
		uint8(AttrFloorRequestStatus), // Type 17
		statusLen,                      // Length
	}
	attr = append(attr, floorAttr...)

	// Pad to 4-byte boundary if needed
	if padding := len(attr) % 4; padding != 0 {
		attr = append(attr, make([]byte, 4-padding)...)
	}

	return attr
}

// AddGroupedAttribute adds a grouped attribute containing nested attributes
// Grouped attributes encode their sub-attributes in the value field
func (m *Message) AddGroupedAttribute(attrType AttributeType, subAttributes []Attribute) {
	// Calculate total length of sub-attributes
	var value []byte
	for _, attr := range subAttributes {
		// Each sub-attribute: Type(1) + Length(1) + Value(Length)
		attrBytes := make([]byte, 2+len(attr.Value))
		attrBytes[0] = uint8(attr.Type)
		attrBytes[1] = attr.Length
		copy(attrBytes[2:], attr.Value)

		// Pad to 4-byte boundary
		if padding := len(attrBytes) % 4; padding != 0 {
			paddingBytes := make([]byte, 4-padding)
			attrBytes = append(attrBytes, paddingBytes...)
		}

		value = append(value, attrBytes...)
	}
	m.AddAttribute(attrType, value)
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
		// RFC 4582 v1: Length includes Type(1) + Length(1) + Value
		// RFC 8855 v2: Length is only the value size
		lengthField := attr.Length
		if m.Version == ProtocolVersionRFC4582 {
			lengthField = attr.Length + 2 // Add 2 for Type+Length header
		}
		buf[offset] = lengthField
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

	// Accept both RFC 4582 (v1) and RFC 8855 (v2) - wire format is compatible
	if msg.Version != ProtocolVersionRFC4582 && msg.Version != ProtocolVersionRFC8855 {
		return nil, fmt.Errorf("unsupported BFCP version: %d (expected %d or %d)", msg.Version, ProtocolVersionRFC4582, ProtocolVersionRFC8855)
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

	log.Printf("🔍 [Decode] Parsing %s message: version=%d, payloadLength=%d words (%d bytes), confID=%d, txID=%d, userID=%d, totalBytes=%d",
		msg.Primitive, msg.Version, msg.PayloadLength, expectedPayloadSize, msg.ConferenceID, msg.TransactionID, msg.UserID, len(data))

	// Parse attributes
	offset := CommonHeaderLength
	endOffset := CommonHeaderLength + expectedPayloadSize

	log.Printf("🔍 [Decode] Starting attribute parsing: offset=%d, endOffset=%d", offset, endOffset)

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

		// RFC 4582 (v1) vs RFC 8855 (v2) difference:
		// - v1: Length includes Type+Length header (total attribute size)
		// - v2: Length is only the value size (excludes Type+Length header)
		var valueLength int
		if msg.Version == ProtocolVersionRFC4582 {
			// v1: Length includes Type(1) + Length(1) = subtract 2
			if attrLength < 2 {
				log.Printf("❌ [Decode] ERROR: RFC 4582 attribute length too small: %d (must be >= 2)", attrLength)
				log.Printf("📋 [Decode] Full message hex dump (%d bytes): %X", len(data), data)
				return nil, fmt.Errorf("invalid RFC 4582 attribute length: %d", attrLength)
			}
			valueLength = int(attrLength) - 2
		} else {
			// v2: Length is the value size directly
			valueLength = int(attrLength)
		}

		log.Printf("🔍 [Decode] Parsing attribute: type=%s(%d), length=%d bytes (v%d format), valueLength=%d, offset=%d, endOffset=%d, remaining=%d",
			attrType, attrType, attrLength, msg.Version, valueLength, offset, endOffset, endOffset-offset)

		// Value (valueLength bytes)
		if offset+valueLength > endOffset {
			log.Printf("❌ [Decode] ERROR: Attribute value exceeds bounds: need %d bytes, have %d bytes remaining",
				valueLength, endOffset-offset)
			// Dump the message for debugging
			log.Printf("📋 [Decode] Full message hex dump (%d bytes): %X", len(data), data)
			return nil, fmt.Errorf("attribute value exceeds message bounds at offset %d", offset)
		}

		value := make([]byte, valueLength)
		copy(value, data[offset:offset+valueLength])
		offset += valueLength

		msg.Attributes = append(msg.Attributes, Attribute{
			Type:   attrType,
			Length: uint8(valueLength), // Store normalized value length
			Value:  value,
		})

		// Skip padding to 4-byte boundary
		// For v1: padding is based on original attrLength
		// For v2: padding is based on Type+Length+Value = 2+valueLength
		var attrTotalLen int
		if msg.Version == ProtocolVersionRFC4582 {
			attrTotalLen = int(attrLength)
		} else {
			attrTotalLen = 2 + valueLength
		}
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
