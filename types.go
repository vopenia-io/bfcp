// Package bfcp implements the Binary Floor Control Protocol (BFCP) as defined in RFC 8855 and RFC 8856.
// It provides both client and server roles for managing floor control in multimedia conferences.
package bfcp

import "fmt"

// BFCP Protocol Versions
const (
	ProtocolVersionRFC4582 = 1 // RFC 4582 (original BFCP)
	ProtocolVersionRFC8855 = 2 // RFC 8855 (updated BFCP)
	ProtocolVersion        = ProtocolVersionRFC8855 // Default version for outgoing messages
)

// Common field lengths
const (
	CommonHeaderLength = 12 // Version(1) + Primitive(1) + Length(2) + ConferenceID(4) + TransactionID(2) + UserID(2)
)

// Primitive represents BFCP message types as defined in RFC 8855 Section 5.1
type Primitive uint8

const (
	PrimitiveFloorRequest         Primitive = 1
	PrimitiveFloorRelease         Primitive = 2
	PrimitiveFloorRequestQuery    Primitive = 3
	PrimitiveFloorRequestStatus   Primitive = 4
	PrimitiveUserQuery            Primitive = 5
	PrimitiveUserStatus           Primitive = 6
	PrimitiveFloorQuery           Primitive = 7
	PrimitiveFloorStatus          Primitive = 8
	PrimitiveChairAction          Primitive = 9
	PrimitiveChairActionAck       Primitive = 10
	PrimitiveHello                Primitive = 11
	PrimitiveHelloAck             Primitive = 12
	PrimitiveError                Primitive = 13
	PrimitiveFloorRequestStatusAck Primitive = 14
	PrimitiveFloorStatusAck       Primitive = 15
	PrimitiveGoodbye              Primitive = 16
	PrimitiveGoodbyeAck           Primitive = 17
)

func (p Primitive) String() string {
	switch p {
	case PrimitiveFloorRequest:
		return "FloorRequest"
	case PrimitiveFloorRelease:
		return "FloorRelease"
	case PrimitiveFloorRequestQuery:
		return "FloorRequestQuery"
	case PrimitiveFloorRequestStatus:
		return "FloorRequestStatus"
	case PrimitiveUserQuery:
		return "UserQuery"
	case PrimitiveUserStatus:
		return "UserStatus"
	case PrimitiveFloorQuery:
		return "FloorQuery"
	case PrimitiveFloorStatus:
		return "FloorStatus"
	case PrimitiveChairAction:
		return "ChairAction"
	case PrimitiveChairActionAck:
		return "ChairActionAck"
	case PrimitiveHello:
		return "Hello"
	case PrimitiveHelloAck:
		return "HelloAck"
	case PrimitiveError:
		return "Error"
	case PrimitiveFloorRequestStatusAck:
		return "FloorRequestStatusAck"
	case PrimitiveFloorStatusAck:
		return "FloorStatusAck"
	case PrimitiveGoodbye:
		return "Goodbye"
	case PrimitiveGoodbyeAck:
		return "GoodbyeAck"
	default:
		return fmt.Sprintf("Unknown(%d)", p)
	}
}

// AttributeType represents BFCP attribute types as defined in RFC 8855 Section 5.2
type AttributeType uint8

const (
	AttrBeneficiaryID          AttributeType = 1
	AttrFloorID                AttributeType = 2
	AttrFloorRequestID         AttributeType = 3
	AttrPriority               AttributeType = 4
	AttrRequestStatus          AttributeType = 5
	AttrErrorCode              AttributeType = 6
	AttrErrorInfo              AttributeType = 7
	AttrParticipantProvidedInfo AttributeType = 8
	AttrStatusInfo             AttributeType = 9
	AttrSupportedAttributes    AttributeType = 10
	AttrSupportedPrimitives    AttributeType = 11
	AttrUserDisplayName        AttributeType = 12
	AttrUserURI                AttributeType = 13
	AttrBeneficiaryInfo        AttributeType = 14
	AttrFloorRequestInfo       AttributeType = 15
	AttrRequestedByInfo        AttributeType = 16
	AttrFloorRequestStatus     AttributeType = 17
	AttrOverallRequestStatus   AttributeType = 18
)

func (a AttributeType) String() string {
	switch a {
	case AttrBeneficiaryID:
		return "BeneficiaryID"
	case AttrFloorID:
		return "FloorID"
	case AttrFloorRequestID:
		return "FloorRequestID"
	case AttrPriority:
		return "Priority"
	case AttrRequestStatus:
		return "RequestStatus"
	case AttrErrorCode:
		return "ErrorCode"
	case AttrErrorInfo:
		return "ErrorInfo"
	case AttrParticipantProvidedInfo:
		return "ParticipantProvidedInfo"
	case AttrStatusInfo:
		return "StatusInfo"
	case AttrSupportedAttributes:
		return "SupportedAttributes"
	case AttrSupportedPrimitives:
		return "SupportedPrimitives"
	case AttrUserDisplayName:
		return "UserDisplayName"
	case AttrUserURI:
		return "UserURI"
	case AttrBeneficiaryInfo:
		return "BeneficiaryInfo"
	case AttrFloorRequestInfo:
		return "FloorRequestInfo"
	case AttrRequestedByInfo:
		return "RequestedByInfo"
	case AttrFloorRequestStatus:
		return "FloorRequestStatus"
	case AttrOverallRequestStatus:
		return "OverallRequestStatus"
	default:
		return fmt.Sprintf("Unknown(%d)", a)
	}
}

// RequestStatus represents the status of a floor request (RFC 8855 Section 5.2.5)
type RequestStatus uint8

const (
	RequestStatusPending   RequestStatus = 1
	RequestStatusAccepted  RequestStatus = 2
	RequestStatusGranted   RequestStatus = 3
	RequestStatusDenied    RequestStatus = 4
	RequestStatusCancelled RequestStatus = 5
	RequestStatusReleased  RequestStatus = 6
	RequestStatusRevoked   RequestStatus = 7
)

func (r RequestStatus) String() string {
	switch r {
	case RequestStatusPending:
		return "Pending"
	case RequestStatusAccepted:
		return "Accepted"
	case RequestStatusGranted:
		return "Granted"
	case RequestStatusDenied:
		return "Denied"
	case RequestStatusCancelled:
		return "Cancelled"
	case RequestStatusReleased:
		return "Released"
	case RequestStatusRevoked:
		return "Revoked"
	default:
		return fmt.Sprintf("Unknown(%d)", r)
	}
}

// ErrorCode represents BFCP error codes (RFC 8855 Section 5.2.6)
type ErrorCode uint8

const (
	ErrorConferenceDoesNotExist       ErrorCode = 1
	ErrorUserDoesNotExist             ErrorCode = 2
	ErrorUnknownPrimitive             ErrorCode = 3
	ErrorUnknownMandatoryAttribute    ErrorCode = 4
	ErrorUnauthorizedOperation        ErrorCode = 5
	ErrorInvalidFloorID               ErrorCode = 6
	ErrorFloorRequestIDDoesNotExist   ErrorCode = 7
	ErrorMaximumFloorsReached         ErrorCode = 8
	ErrorUseBFCP                      ErrorCode = 9
	ErrorFloorReleaseDenied           ErrorCode = 10
	ErrorInsufficientUserPrivileges   ErrorCode = 11
)

func (e ErrorCode) String() string {
	switch e {
	case ErrorConferenceDoesNotExist:
		return "ConferenceDoesNotExist"
	case ErrorUserDoesNotExist:
		return "UserDoesNotExist"
	case ErrorUnknownPrimitive:
		return "UnknownPrimitive"
	case ErrorUnknownMandatoryAttribute:
		return "UnknownMandatoryAttribute"
	case ErrorUnauthorizedOperation:
		return "UnauthorizedOperation"
	case ErrorInvalidFloorID:
		return "InvalidFloorID"
	case ErrorFloorRequestIDDoesNotExist:
		return "FloorRequestIDDoesNotExist"
	case ErrorMaximumFloorsReached:
		return "MaximumFloorsReached"
	case ErrorUseBFCP:
		return "UseBFCP"
	case ErrorFloorReleaseDenied:
		return "FloorReleaseDenied"
	case ErrorInsufficientUserPrivileges:
		return "InsufficientUserPrivileges"
	default:
		return fmt.Sprintf("Unknown(%d)", e)
	}
}

// Priority represents floor request priority (RFC 8855 Section 5.2.4)
type Priority uint16

const (
	PriorityLowest  Priority = 0
	PriorityLow     Priority = 256
	PriorityNormal  Priority = 512
	PriorityHigh    Priority = 768
	PriorityHighest Priority = 1023
)

// FloorState represents the current state of a floor in the floor control server
type FloorState struct {
	FloorID        uint16
	OwnerUserID    uint16
	FloorRequestID uint16
	Status         RequestStatus
	LastTxID       uint16
	Priority       Priority
}

// SessionState represents the BFCP session state machine
type SessionState uint8

const (
	StateDisconnected     SessionState = 0
	StateConnected        SessionState = 1
	StateWaitHelloAck     SessionState = 2
	StateWaitFloorRequest SessionState = 3
	StateFloorRequested   SessionState = 4
	StateFloorGranted     SessionState = 5
	StateFloorDenied      SessionState = 6
	StateFloorReleased    SessionState = 7
)

func (s SessionState) String() string {
	switch s {
	case StateDisconnected:
		return "Disconnected"
	case StateConnected:
		return "Connected"
	case StateWaitHelloAck:
		return "WaitHelloAck"
	case StateWaitFloorRequest:
		return "WaitFloorRequest"
	case StateFloorRequested:
		return "FloorRequested"
	case StateFloorGranted:
		return "FloorGranted"
	case StateFloorDenied:
		return "FloorDenied"
	case StateFloorReleased:
		return "FloorReleased"
	default:
		return fmt.Sprintf("Unknown(%d)", s)
	}
}
