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
	RawTLV bool   // If true, Value contains a complete pre-encoded TLV (Type+Length+Value+padding)
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

// buildSimpleAttr builds a simple (non-grouped) BFCP attribute with proper RFC 4582 v1 encoding.
// Per RFC 4582, Length is in bytes and includes Type+Length+Value+padding.
// Attribute is padded to 4-byte boundary.
func buildSimpleAttr(attrType uint8, mandatory bool, value []byte) []byte {
	header := attrType << 1 // shift type up, bit0 = M
	if mandatory {
		header |= 0x01
	}
	// base length: 2-byte header + len(value)
	l := 2 + len(value)
	// pad to 4-byte boundary
	pad := (4 - (l % 4)) % 4
	total := l + pad

	out := make([]byte, total)
	out[0] = header
	out[1] = byte(total) // RFC 4582 v1: length in bytes (includes Type+Length+Value+padding)
	copy(out[2:], value)
	// padding is already zero
	return out
}

// buildGroupedAttr builds a grouped BFCP attribute from already-encoded children.
// Per RFC 4582 v1, Length is in bytes and includes Type+Length+children+padding.
// Attribute is padded to 4-byte boundary.
// NOTE: This is for simple grouped attributes WITHOUT a header ID field.
// For attributes like FLOOR-REQUEST-INFORMATION, FLOOR-REQUEST-STATUS, OVERALL-REQUEST-STATUS
// that have a 2-byte header ID, use buildGroupedAttrWithHeaderID instead.
func buildGroupedAttr(attrType uint8, mandatory bool, children [][]byte) []byte {
	header := attrType << 1
	if mandatory {
		header |= 0x01
	}
	// 2-byte header + sum(children)
	size := 2
	for _, c := range children {
		size += len(c)
	}
	// pad to 4-byte boundary
	pad := (4 - (size % 4)) % 4
	total := size + pad

	out := make([]byte, total)
	out[0] = header
	out[1] = byte(total) // RFC 4582 v1: length in bytes (includes Type+Length+children+padding)
	off := 2
	for _, c := range children {
		copy(out[off:], c)
		off += len(c)
	}
	// padding zeroed by make
	return out
}

// buildGroupedAttrWithHeaderID builds a grouped BFCP attribute with a 2-byte header ID.
// Per RFC 8855, certain grouped attributes have a 2-byte ID field directly in their header,
// BEFORE any sub-attributes:
//   - FLOOR-REQUEST-INFORMATION (type 31): Floor Request ID
//   - FLOOR-REQUEST-STATUS (type 33): Floor ID
//   - OVERALL-REQUEST-STATUS (type 35): Floor Request ID
//
// Structure:
//
//	0                   1                   2                   3
//	0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|  Type | M | R |     Length    |     Header ID (16-bit)        |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|           sub-attributes...                                   |
//
// Length is in bytes and includes Type+Length+HeaderID+children (RFC 4582 v1 format).
func buildGroupedAttrWithHeaderID(attrType uint8, mandatory bool, headerID uint16, children [][]byte) []byte {
	header := attrType << 1
	if mandatory {
		header |= 0x01
	}
	// 2-byte TLV header + 2-byte header ID + sum(children)
	size := 4
	for _, c := range children {
		size += len(c)
	}
	// pad to 4-byte boundary
	pad := (4 - (size % 4)) % 4
	total := size + pad

	out := make([]byte, total)
	out[0] = header
	out[1] = byte(total) // RFC 4582 v1: length in bytes (includes Type+Length+HeaderID+children+padding)
	// Header ID in big-endian
	out[2] = byte(headerID >> 8)
	out[3] = byte(headerID & 0xFF)
	off := 4
	for _, c := range children {
		copy(out[off:], c)
		off += len(c)
	}
	// padding zeroed by make
	return out
}

// BuildFloorStatusMessage builds a complete FloorStatus message with proper RFC 8855 encoding.
// This bypasses the normal Message.Encode() path to ensure correct encoding.
//
// Structure per RFC 8855:
//
//	FloorStatus:
//	  FLOOR-ID (top-level, simple attr)
//	  FLOOR-REQUEST-INFORMATION (grouped with Floor Request ID in header):
//	    OVERALL-REQUEST-STATUS (grouped with Floor Request ID in header):
//	      REQUEST-STATUS
//	    FLOOR-REQUEST-STATUS (grouped with Floor ID in header):
//	      REQUEST-STATUS
//
// Key difference from old code: FLOOR-REQUEST-INFORMATION, FLOOR-REQUEST-STATUS, and
// OVERALL-REQUEST-STATUS have 2-byte header IDs directly in their TLV structure,
// BEFORE any sub-attributes.
func BuildFloorStatusMessage(confID uint32, txID uint16, userID uint16, floorID uint16, requestID uint16, status RequestStatus) []byte {
	// 1) FLOOR-ID (top-level, mandatory, simple attribute)
	floorIDVal := make([]byte, 2)
	binary.BigEndian.PutUint16(floorIDVal, floorID)
	floorIDAttr := buildSimpleAttr(uint8(AttrFloorID), true, floorIDVal)

	// 2) REQUEST-STATUS (child of OVERALL-REQUEST-STATUS and FLOOR-REQUEST-STATUS)
	// REQUEST-STATUS value: 2 bytes - status in bits, queue position
	// Per RFC 8855, REQUEST-STATUS is only 2 bytes (status + queue pos)
	reqStatusVal := make([]byte, 2)
	reqStatusVal[0] = uint8(status) // status value directly
	reqStatusVal[1] = 0             // queue position = 0
	requestStatusAttr := buildSimpleAttr(uint8(AttrRequestStatus), false, reqStatusVal)

	// 3) OVERALL-REQUEST-STATUS (grouped with Floor Request ID in header)
	// Per RFC 8855, OVERALL-REQUEST-STATUS has Floor Request ID (2 bytes) in header
	// Contains: REQUEST-STATUS sub-attribute
	overallReqStatusAttr := buildGroupedAttrWithHeaderID(uint8(AttrOverallRequestStatus), false, requestID, [][]byte{
		requestStatusAttr,
	})

	// 4) FLOOR-REQUEST-STATUS (grouped with Floor ID in header)
	// Per RFC 8855, FLOOR-REQUEST-STATUS has Floor ID (2 bytes) in header
	// Contains: REQUEST-STATUS sub-attribute (no nested FLOOR-ID needed - it's in header)
	floorReqStatusAttr := buildGroupedAttrWithHeaderID(uint8(AttrFloorRequestStatus), false, floorID, [][]byte{
		requestStatusAttr, // Same status for this floor
	})

	// 5) FLOOR-REQUEST-INFORMATION (grouped with Floor Request ID in header)
	// Per RFC 8855, FLOOR-REQUEST-INFORMATION has Floor Request ID (2 bytes) in header
	// Contains: OVERALL-REQUEST-STATUS, FLOOR-REQUEST-STATUS
	floorReqInfoAttr := buildGroupedAttrWithHeaderID(uint8(AttrFloorRequestInfo), true, requestID, [][]byte{
		overallReqStatusAttr,
		floorReqStatusAttr,
	})

	// Top-level payload: FLOOR-ID + FLOOR-REQUEST-INFORMATION
	payload := append(floorIDAttr, floorReqInfoAttr...)

	// Build complete message with common header
	// Header is 12 bytes, length is in 32-bit words
	lengthWords := uint16(len(payload) / 4)
	buf := make([]byte, 12+len(payload))

	// Byte 0: Version(3 bits) + Reserved(5 bits) - Version 1 = 0x20
	buf[0] = 0x20 // Version 1

	// Byte 1: Primitive (FloorStatus = 8)
	buf[1] = uint8(PrimitiveFloorStatus)

	// Bytes 2-3: PayloadLength in 32-bit words
	binary.BigEndian.PutUint16(buf[2:4], lengthWords)

	// Bytes 4-7: ConferenceID
	binary.BigEndian.PutUint32(buf[4:8], confID)

	// Bytes 8-9: TransactionID
	binary.BigEndian.PutUint16(buf[8:10], txID)

	// Bytes 10-11: UserID
	binary.BigEndian.PutUint16(buf[10:12], userID)

	// Copy payload
	copy(buf[12:], payload)

	return buf
}

// AddFloorRequestInformationRFC4582 adds a FLOOR-REQUEST-INFORMATION grouped attribute
// with proper RFC 8855 encoding (length in 32-bit words, header IDs).
//
// Structure per RFC 8855:
//
//	FLOOR-REQUEST-INFORMATION (grouped with Floor Request ID in header):
//	  OVERALL-REQUEST-STATUS (grouped with Floor Request ID in header):
//	    REQUEST-STATUS
//	  FLOOR-REQUEST-STATUS (grouped with Floor ID in header):
//	    REQUEST-STATUS
func (m *Message) AddFloorRequestInformationRFC4582(requestID uint16, status RequestStatus, floorID uint16) {
	// Build the nested structure from inside out

	// 1) REQUEST-STATUS (2 bytes: status + queue position)
	reqStatusVal := make([]byte, 2)
	reqStatusVal[0] = uint8(status) // status value directly
	reqStatusVal[1] = 0             // queue position = 0
	requestStatusAttr := buildSimpleAttr(uint8(AttrRequestStatus), false, reqStatusVal)

	// 2) OVERALL-REQUEST-STATUS (grouped with Floor Request ID in header)
	overallReqStatusAttr := buildGroupedAttrWithHeaderID(uint8(AttrOverallRequestStatus), false, requestID, [][]byte{
		requestStatusAttr,
	})

	// 3) FLOOR-REQUEST-STATUS (grouped with Floor ID in header)
	floorReqStatusAttr := buildGroupedAttrWithHeaderID(uint8(AttrFloorRequestStatus), false, floorID, [][]byte{
		requestStatusAttr,
	})

	// 4) FLOOR-REQUEST-INFORMATION (grouped with Floor Request ID in header)
	floorReqInfoAttr := buildGroupedAttrWithHeaderID(uint8(AttrFloorRequestInfo), true, requestID, [][]byte{
		overallReqStatusAttr,
		floorReqStatusAttr,
	})

	// Add as raw bytes (already properly encoded with length in words)
	// RawTLV=true tells Encode() to copy this as-is without re-encoding
	m.Attributes = append(m.Attributes, Attribute{
		Type:   AttrFloorRequestInfo,
		Length: uint8(len(floorReqInfoAttr)), // Store raw length for internal use
		Value:  floorReqInfoAttr,             // Store full encoded attribute
		RawTLV: true,                         // This is a complete pre-encoded TLV
	})
}

// AddFloorRequestInformation adds a FLOOR-REQUEST-INFORMATION grouped attribute
// containing the floor request ID, overall status, and floor-specific statuses.
// DEPRECATED: Use AddFloorRequestInformationRFC4582 for proper RFC 4582 encoding.
func (m *Message) AddFloorRequestInformation(requestID uint16, status RequestStatus, queuePos uint8, floorIDs ...uint16) {
	if len(floorIDs) > 0 {
		// Use new RFC 4582 compliant encoding
		m.AddFloorRequestInformationRFC4582(requestID, status, floorIDs[0])
		return
	}
}

// AddGroupedAttribute adds a grouped attribute containing nested attributes
// Grouped attributes encode their sub-attributes in the value field
func (m *Message) AddGroupedAttribute(attrType AttributeType, subAttributes []Attribute) {
	// Calculate total length of sub-attributes
	var value []byte
	for _, attr := range subAttributes {
		// Each sub-attribute: Type(1) + Length(1) + Value(Length)
		// RFC 4582/8855: Type byte = (type << 1) | mandatory_bit
		attrBytes := make([]byte, 2+len(attr.Value))
		attrBytes[0] = (uint8(attr.Type) << 1) | 0x01 // Encode type with mandatory bit
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
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|Ver|  Resv |P|  Primitive |            Length                   |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|         ConferenceID          |        TransactionID          |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|           UserID              |       (Start of Attributes)   |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
func (m *Message) Encode() ([]byte, error) {
	// Calculate total attribute length
	attrLength := 0
	for _, attr := range m.Attributes {
		if attr.RawTLV {
			// RawTLV: Value contains the complete pre-encoded TLV (already padded)
			attrLength += len(attr.Value)
		} else {
			// Normal attribute: Type(1) + Length(1) + Value(Length) + padding to 4-byte boundary
			attrLen := 2 + int(attr.Length)
			// Pad to 4-byte boundary
			if padding := attrLen % 4; padding != 0 {
				attrLen += 4 - padding
			}
			attrLength += attrLen
		}
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
		if attr.RawTLV {
			// RawTLV: Copy the pre-encoded TLV as-is (no re-encoding)
			copy(buf[offset:], attr.Value)
			offset += len(attr.Value)
		} else {
			// Normal attribute encoding
			// Type (1 byte) - RFC 4582/8855: 7-bit type + 1-bit mandatory flag
			// Format: | Type (7 bits) | M (1 bit) |
			// Encode as: (type << 1) | mandatory_bit
			// All our attributes are mandatory (M=1) for now
			buf[offset] = (uint8(attr.Type) << 1) | 0x01
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

		// Type (1 byte) - RFC 4582/8855: 7-bit type + 1-bit mandatory flag
		// Format: | Type (7 bits) | M (1 bit) |
		// Decode as: type = byte >> 1, mandatory = byte & 0x01
		typeByte := data[offset]
		attrType := AttributeType(typeByte >> 1)
		// mandatory := (typeByte & 0x01) != 0 // Not used yet, but available
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
