// Automatically generated by MockGen. DO NOT EDIT!
// Source: ./jobcontrol.go

package controller

import (
	gomock "github.com/golang/mock/gomock"
	v1 "k8s.io/api/batch/v1"
	v10 "k8s.io/api/core/v1"
	v11 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Mock of JobControl interface
type MockJobControl struct {
	ctrl     *gomock.Controller
	recorder *_MockJobControlRecorder
}

// Recorder for MockJobControl (not exported)
type _MockJobControlRecorder struct {
	mock *MockJobControl
}

func NewMockJobControl(ctrl *gomock.Controller) *MockJobControl {
	mock := &MockJobControl{ctrl: ctrl}
	mock.recorder = &_MockJobControlRecorder{mock}
	return mock
}

func (_m *MockJobControl) EXPECT() *_MockJobControlRecorder {
	return _m.recorder
}

func (_m *MockJobControl) OnAdd(obj interface{}) {
	_m.ctrl.Call(_m, "OnAdd", obj)
}

func (_mr *_MockJobControlRecorder) OnAdd(arg0 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "OnAdd", arg0)
}

func (_m *MockJobControl) OnUpdate(oldObj interface{}, newObj interface{}) {
	_m.ctrl.Call(_m, "OnUpdate", oldObj, newObj)
}

func (_mr *_MockJobControlRecorder) OnUpdate(arg0, arg1 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "OnUpdate", arg0, arg1)
}

func (_m *MockJobControl) OnDelete(obj interface{}) {
	_m.ctrl.Call(_m, "OnDelete", obj)
}

func (_mr *_MockJobControlRecorder) OnDelete(arg0 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "OnDelete", arg0)
}

func (_m *MockJobControl) ControlJobs(ownerKey string, owner v11.Object, buildNewJob bool, jobFactory JobFactory) (JobControlResult, *v1.Job, error) {
	ret := _m.ctrl.Call(_m, "ControlJobs", ownerKey, owner, buildNewJob, jobFactory)
	ret0, _ := ret[0].(JobControlResult)
	ret1, _ := ret[1].(*v1.Job)
	ret2, _ := ret[2].(error)
	return ret0, ret1, ret2
}

func (_mr *_MockJobControlRecorder) ControlJobs(arg0, arg1, arg2, arg3 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "ControlJobs", arg0, arg1, arg2, arg3)
}

func (_m *MockJobControl) ObserveOwnerDeletion(ownerKey string) {
	_m.ctrl.Call(_m, "ObserveOwnerDeletion", ownerKey)
}

func (_mr *_MockJobControlRecorder) ObserveOwnerDeletion(arg0 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "ObserveOwnerDeletion", arg0)
}

func (_m *MockJobControl) GetJobPrefix() string {
	ret := _m.ctrl.Call(_m, "GetJobPrefix")
	ret0, _ := ret[0].(string)
	return ret0
}

func (_mr *_MockJobControlRecorder) GetJobPrefix() *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "GetJobPrefix")
}

// Mock of JobOwnerControl interface
type MockJobOwnerControl struct {
	ctrl     *gomock.Controller
	recorder *_MockJobOwnerControlRecorder
}

// Recorder for MockJobOwnerControl (not exported)
type _MockJobOwnerControlRecorder struct {
	mock *MockJobOwnerControl
}

func NewMockJobOwnerControl(ctrl *gomock.Controller) *MockJobOwnerControl {
	mock := &MockJobOwnerControl{ctrl: ctrl}
	mock.recorder = &_MockJobOwnerControlRecorder{mock}
	return mock
}

func (_m *MockJobOwnerControl) EXPECT() *_MockJobOwnerControlRecorder {
	return _m.recorder
}

func (_m *MockJobOwnerControl) GetOwnerKey(owner v11.Object) (string, error) {
	ret := _m.ctrl.Call(_m, "GetOwnerKey", owner)
	ret0, _ := ret[0].(string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

func (_mr *_MockJobOwnerControlRecorder) GetOwnerKey(arg0 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "GetOwnerKey", arg0)
}

func (_m *MockJobOwnerControl) GetOwner(namespace string, name string) (v11.Object, error) {
	ret := _m.ctrl.Call(_m, "GetOwner", namespace, name)
	ret0, _ := ret[0].(v11.Object)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

func (_mr *_MockJobOwnerControlRecorder) GetOwner(arg0, arg1 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "GetOwner", arg0, arg1)
}

func (_m *MockJobOwnerControl) OnOwnedJobEvent(owner v11.Object) {
	_m.ctrl.Call(_m, "OnOwnedJobEvent", owner)
}

func (_mr *_MockJobOwnerControlRecorder) OnOwnedJobEvent(arg0 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "OnOwnedJobEvent", arg0)
}

// Mock of JobFactory interface
type MockJobFactory struct {
	ctrl     *gomock.Controller
	recorder *_MockJobFactoryRecorder
}

// Recorder for MockJobFactory (not exported)
type _MockJobFactoryRecorder struct {
	mock *MockJobFactory
}

func NewMockJobFactory(ctrl *gomock.Controller) *MockJobFactory {
	mock := &MockJobFactory{ctrl: ctrl}
	mock.recorder = &_MockJobFactoryRecorder{mock}
	return mock
}

func (_m *MockJobFactory) EXPECT() *_MockJobFactoryRecorder {
	return _m.recorder
}

func (_m *MockJobFactory) BuildJob(name string) (*v1.Job, *v10.ConfigMap, error) {
	ret := _m.ctrl.Call(_m, "BuildJob", name)
	ret0, _ := ret[0].(*v1.Job)
	ret1, _ := ret[1].(*v10.ConfigMap)
	ret2, _ := ret[2].(error)
	return ret0, ret1, ret2
}

func (_mr *_MockJobFactoryRecorder) BuildJob(arg0 interface{}) *gomock.Call {
	return _mr.mock.ctrl.RecordCall(_mr.mock, "BuildJob", arg0)
}
