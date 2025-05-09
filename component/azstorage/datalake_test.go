//go:build !authtest
// +build !authtest

/*
    _____           _____   _____   ____          ______  _____  ------
   |     |  |      |     | |     | |     |     | |       |            |
   |     |  |      |     | |     | |     |     | |       |            |
   | --- |  |      |     | |-----| |---- |     | |-----| |-----  ------
   |     |  |      |     | |     | |     |     |       | |       |
   | ____|  |_____ | ____| | ____| |     |_____|  _____| |_____  |_____


   Licensed under the MIT License <http://opensource.org/licenses/MIT>.

   Copyright © 2020-2025 Microsoft Corporation. All rights reserved.
   Author : <blobfusedev@microsoft.com>

   Permission is hereby granted, free of charge, to any person obtaining a copy
   of this software and associated documentation files (the "Software"), to deal
   in the Software without restriction, including without limitation the rights
   to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
   copies of the Software, and to permit persons to whom the Software is
   furnished to do so, subject to the following conditions:

   The above copyright notice and this permission notice shall be included in all
   copies or substantial portions of the Software.

   THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
   IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
   FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
   AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
   LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
   OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
   SOFTWARE
*/

package azstorage

import (
	"bytes"
	"container/list"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azdatalake/directory"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azdatalake/file"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azdatalake/filesystem"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azdatalake/service"

	"github.com/Azure/azure-storage-fuse/v2/common"
	"github.com/Azure/azure-storage-fuse/v2/common/log"
	"github.com/Azure/azure-storage-fuse/v2/internal"
	"github.com/Azure/azure-storage-fuse/v2/internal/handlemap"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type datalakeTestSuite struct {
	suite.Suite
	assert          *assert.Assertions
	az              *AzStorage
	serviceClient   *service.Client
	containerClient *filesystem.Client
	config          string
	container       string
}

func (s *datalakeTestSuite) SetupTest() {
	// Logging config
	cfg := common.LogConfig{
		FilePath:    "./logfile.txt",
		MaxFileSize: 10,
		FileCount:   10,
		Level:       common.ELogLevel.LOG_DEBUG(),
	}
	log.SetDefaultLogger("base", cfg)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Println("Unable to get home directory")
		os.Exit(1)
	}
	cfgFile, err := os.Open(homeDir + "/azuretest.json")
	if err != nil {
		fmt.Println("Unable to open config file")
		os.Exit(1)
	}

	cfgData, _ := io.ReadAll(cfgFile)
	err = json.Unmarshal(cfgData, &storageTestConfigurationParameters)
	if err != nil {
		fmt.Println("Failed to parse the config file")
		os.Exit(1)
	}

	cfgFile.Close()
	s.setupTestHelper("", "", true)
}

func (s *datalakeTestSuite) setupTestHelper(configuration string, container string, create bool) {
	if container == "" {
		container = generateContainerName()
	}
	s.container = container
	if configuration == "" {
		configuration = fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  fail-unsupported-op: true",
			storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container)
	}
	s.config = configuration
	s.assert = assert.New(s.T())

	s.az, _ = newTestAzStorage(configuration)
	s.az.Start(ctx) // Note: Start->TestValidation will fail but it doesn't matter. We are creating the container a few lines below anyway.
	// We could create the container before but that requires rewriting the code to new up a service client.

	s.serviceClient = s.az.storage.(*Datalake).Service // Grab the service client to do some validation
	s.containerClient = s.serviceClient.NewFileSystemClient(s.container)
	if create {
		s.containerClient.Create(ctx, nil)
	}
}

func (s *datalakeTestSuite) tearDownTestHelper(delete bool) {
	s.az.Stop()
	if delete {
		s.containerClient.Delete(ctx, nil)
	}
}

func (s *datalakeTestSuite) cleanupTest() {
	s.tearDownTestHelper(true)
	log.Destroy()
}

func (s *datalakeTestSuite) TestDefault() {
	defer s.cleanupTest()
	s.assert.Equal(storageTestConfigurationParameters.AdlsAccount, s.az.stConfig.authConfig.AccountName)
	s.assert.Equal(EAccountType.ADLS(), s.az.stConfig.authConfig.AccountType)
	s.assert.False(s.az.stConfig.authConfig.UseHTTP)
	s.assert.Equal(storageTestConfigurationParameters.AdlsKey, s.az.stConfig.authConfig.AccountKey)
	s.assert.Empty(s.az.stConfig.authConfig.SASKey)
	s.assert.Empty(s.az.stConfig.authConfig.ApplicationID)
	s.assert.Empty(s.az.stConfig.authConfig.ResourceID)
	s.assert.Empty(s.az.stConfig.authConfig.ActiveDirectoryEndpoint)
	s.assert.Empty(s.az.stConfig.authConfig.ClientSecret)
	s.assert.Empty(s.az.stConfig.authConfig.TenantID)
	s.assert.Empty(s.az.stConfig.authConfig.ClientID)
	s.assert.EqualValues("https://"+s.az.stConfig.authConfig.AccountName+".dfs.core.windows.net/", s.az.stConfig.authConfig.Endpoint)
	s.assert.Equal(EAuthType.KEY(), s.az.stConfig.authConfig.AuthMode)
	s.assert.Equal(s.container, s.az.stConfig.container)
	s.assert.Empty(s.az.stConfig.prefixPath)
	s.assert.EqualValues(0, s.az.stConfig.blockSize)
	s.assert.EqualValues(32, s.az.stConfig.maxConcurrency)
	s.assert.EqualValues((*blob.AccessTier)(nil), s.az.stConfig.defaultTier)
	s.assert.EqualValues(0, s.az.stConfig.cancelListForSeconds)

	s.assert.EqualValues(5, s.az.stConfig.maxRetries)
	s.assert.EqualValues(900, s.az.stConfig.maxTimeout)
	s.assert.EqualValues(4, s.az.stConfig.backoffTime)
	s.assert.EqualValues(60, s.az.stConfig.maxRetryDelay)

	s.assert.Empty(s.az.stConfig.proxyAddress)
}

func (s *datalakeTestSuite) TestModifyEndpoint() {
	defer s.cleanupTest()
	// Setup
	s.tearDownTestHelper(false) // Don't delete the generated container.
	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.blob.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  fail-unsupported-op: true",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container)
	s.setupTestHelper(config, s.container, true)

	err := s.az.storage.TestPipeline()
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestNoEndpoint() {
	defer s.cleanupTest()
	// Setup
	s.tearDownTestHelper(false) // Don't delete the generated container.
	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  fail-unsupported-op: true",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container)
	s.setupTestHelper(config, s.container, true)

	err := s.az.storage.TestPipeline()
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestAccountType() {
	defer s.cleanupTest()
	// Setup
	s.tearDownTestHelper(false) // Don't delete the generated container.
	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  fail-unsupported-op: true",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container)
	s.setupTestHelper(config, s.container, true)

	val := s.az.storage.IsAccountADLS()
	s.assert.True(val)
}

func (s *datalakeTestSuite) TestFileSystemNotFound() {
	defer s.cleanupTest()
	// Setup
	s.tearDownTestHelper(false) // Don't delete the generated container.
	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  fail-unsupported-op: true",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, "foo")
	s.setupTestHelper(config, "foo", false)

	err := s.az.storage.TestPipeline()
	s.assert.NotNil(err)
	s.assert.Contains(err.Error(), "FilesystemNotFound")
}

func (s *datalakeTestSuite) TestListContainers() {
	defer s.cleanupTest()
	// Setup
	num := 10
	prefix := generateContainerName()
	for i := 0; i < num; i++ {
		f := s.serviceClient.NewFileSystemClient(prefix + fmt.Sprint(i))
		f.Create(ctx, nil)
		defer f.Delete(ctx, nil)
	}

	containers, err := s.az.ListContainers()

	s.assert.Nil(err)
	s.assert.NotNil(containers)
	s.assert.True(len(containers) >= num)
	count := 0
	for _, c := range containers {
		if strings.HasPrefix(c, prefix) {
			count++
		}
	}
	s.assert.EqualValues(num, count)
}

// TODO : ListContainersHuge: Maybe this is overkill?

func (s *datalakeTestSuite) TestCreateDir() {
	defer s.cleanupTest()
	// Testing dir and dir/
	var paths = []string{generateDirectoryName(), generateDirectoryName() + "/"}
	for _, path := range paths {
		log.Debug(path)
		s.Run(path, func() {
			err := s.az.CreateDir(internal.CreateDirOptions{Name: path})

			s.assert.Nil(err)
			// Directory should be in the account
			dir := s.containerClient.NewDirectoryClient(internal.TruncateDirName(path))
			_, err = dir.GetProperties(ctx, nil)
			s.assert.Nil(err)
		})
	}
}

func (s *datalakeTestSuite) TestCreateDirWithCPKEnabled() {
	defer s.cleanupTest()
	CPKEncryptionKey, CPKEncryptionKeySHA256 := generateCPKInfo()

	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  cpk-enabled: true\n  cpk-encryption-key: %s\n  cpk-encryption-key-sha256: %s\n",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container, CPKEncryptionKey, CPKEncryptionKeySHA256)
	s.setupTestHelper(config, s.container, false)

	datalakeCPKOpt := &file.CPKInfo{
		EncryptionKey:       &CPKEncryptionKey,
		EncryptionKeySHA256: &CPKEncryptionKeySHA256,
		EncryptionAlgorithm: to.Ptr(file.EncryptionAlgorithmTypeAES256),
	}

	// Testing dir and dir/
	var paths = []string{generateDirectoryName()}
	for _, path := range paths {
		log.Debug(path)
		s.Run(path, func() {
			err := s.az.CreateDir(internal.CreateDirOptions{Name: path})

			s.assert.Nil(err)
			// Directory should not be accessible without CPK
			dir := s.containerClient.NewDirectoryClient(internal.TruncateDirName(path))
			_, err = dir.GetProperties(ctx, nil)
			s.assert.NotNil(err)

			//Directory should exist
			dir = s.containerClient.NewDirectoryClient(internal.TruncateDirName(path))
			_, err = dir.GetProperties(ctx, &directory.GetPropertiesOptions{
				CPKInfo: datalakeCPKOpt,
			})
			s.assert.Nil(err)
		})
	}
}

func (s *datalakeTestSuite) TestDeleteDir() {
	defer s.cleanupTest()
	// Testing dir and dir/
	var paths = []string{generateDirectoryName(), generateDirectoryName() + "/"}
	for _, path := range paths {
		log.Debug(path)
		s.Run(path, func() {
			s.az.CreateDir(internal.CreateDirOptions{Name: path})

			err := s.az.DeleteDir(internal.DeleteDirOptions{Name: path})

			s.assert.Nil(err)
			// Directory should not be in the account
			dir := s.containerClient.NewDirectoryClient(internal.TruncateDirName(path))
			_, err = dir.GetProperties(ctx, nil)
			s.assert.NotNil(err)
		})
	}
}

// Directory structure
// a/
//  a/c1/
//   a/c1/gc1
//	a/c2
// ab/
//  ab/c1
// ac

func (s *datalakeTestSuite) setupHierarchy(base string) (*list.List, *list.List, *list.List) {
	// Hierarchy looks as follows
	// a/
	//  a/c1/
	//   a/c1/gc1
	//	a/c2
	// ab/
	//  ab/c1
	// ac
	s.az.CreateDir(internal.CreateDirOptions{Name: base})
	c1 := base + "/c1"
	s.az.CreateDir(internal.CreateDirOptions{Name: c1})
	gc1 := c1 + "/gc1"
	s.az.CreateFile(internal.CreateFileOptions{Name: gc1})
	c2 := base + "/c2"
	s.az.CreateFile(internal.CreateFileOptions{Name: c2})
	abPath := base + "b"
	s.az.CreateDir(internal.CreateDirOptions{Name: abPath})
	abc1 := abPath + "/c1"
	s.az.CreateFile(internal.CreateFileOptions{Name: abc1})
	acPath := base + "c"
	s.az.CreateFile(internal.CreateFileOptions{Name: acPath})

	a, ab, ac := generateNestedDirectory(base)

	// Validate the paths were setup correctly and all paths exist
	for p := a.Front(); p != nil; p = p.Next() {
		_, err := s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.Nil(err)
	}
	for p := ab.Front(); p != nil; p = p.Next() {
		_, err := s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.Nil(err)
	}
	for p := ac.Front(); p != nil; p = p.Next() {
		_, err := s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.Nil(err)
	}
	return a, ab, ac
}

func (s *datalakeTestSuite) TestDeleteDirHierarchy() {
	defer s.cleanupTest()
	// Setup
	base := generateDirectoryName()
	a, ab, ac := s.setupHierarchy(base)

	err := s.az.DeleteDir(internal.DeleteDirOptions{Name: base})

	s.assert.Nil(err)

	// a paths should be deleted
	for p := a.Front(); p != nil; p = p.Next() {
		_, err = s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.NotNil(err)
	}
	ab.PushBackList(ac) // ab and ac paths should exist
	for p := ab.Front(); p != nil; p = p.Next() {
		_, err = s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.Nil(err)
	}
}

func (s *datalakeTestSuite) TestDeleteSubDirPrefixPath() {
	defer s.cleanupTest()
	// Setup
	base := generateDirectoryName()
	a, ab, ac := s.setupHierarchy(base)

	s.az.storage.SetPrefixPath(base)

	err := s.az.DeleteDir(internal.DeleteDirOptions{Name: "c1"})
	s.assert.Nil(err)

	// a paths under c1 should be deleted
	for p := a.Front(); p != nil; p = p.Next() {
		path := p.Value.(string)
		_, err = s.containerClient.NewDirectoryClient(path).GetProperties(ctx, nil)
		if strings.HasPrefix(path, base+"/c1") {
			s.assert.NotNil(err)
		} else {
			s.assert.Nil(err)
		}
	}
	ab.PushBackList(ac) // ab and ac paths should exist
	for p := ab.Front(); p != nil; p = p.Next() {
		_, err = s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.Nil(err)
	}
}

func (s *datalakeTestSuite) TestDeleteDirError() {
	defer s.cleanupTest()
	// Setup
	name := generateDirectoryName()

	err := s.az.DeleteDir(internal.DeleteDirOptions{Name: name})

	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)
	// Directory should not be in the account
	dir := s.containerClient.NewDirectoryClient(name)
	_, err = dir.GetProperties(ctx, nil)
	s.assert.NotNil(err)
}

func (s *datalakeTestSuite) TestIsDirEmpty() {
	defer s.cleanupTest()
	// Setup
	name := generateDirectoryName()
	s.az.CreateDir(internal.CreateDirOptions{Name: name})

	// Testing dir and dir/
	var paths = []string{name, name + "/"}
	for _, path := range paths {
		log.Debug(path)
		s.Run(path, func() {
			empty := s.az.IsDirEmpty(internal.IsDirEmptyOptions{Name: name})

			s.assert.True(empty)
		})
	}
}

func (s *datalakeTestSuite) TestIsDirEmptyFalse() {
	defer s.cleanupTest()
	// Setup
	name := generateDirectoryName()
	s.az.CreateDir(internal.CreateDirOptions{Name: name})
	file := name + "/" + generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: file})

	empty := s.az.IsDirEmpty(internal.IsDirEmptyOptions{Name: name})

	s.assert.False(empty)
}

func (s *datalakeTestSuite) TestIsDirEmptyError() {
	defer s.cleanupTest()
	// Setup
	name := generateDirectoryName()

	empty := s.az.IsDirEmpty(internal.IsDirEmptyOptions{Name: name})

	s.assert.True(empty) // Note: See comment in BlockBlob.List. BlockBlob behaves differently from Datalake

	// Directory should not be in the account
	dir := s.containerClient.NewDirectoryClient(name)
	_, err := dir.GetProperties(ctx, nil)
	s.assert.NotNil(err)
}

func (s *datalakeTestSuite) TestReadDir() {
	defer s.cleanupTest()
	// This tests the default listBlocked = 0. It should return the expected paths.
	// Setup
	name := generateDirectoryName()
	s.az.CreateDir(internal.CreateDirOptions{Name: name})
	childName := name + "/" + generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: childName})

	// Testing dir and dir/
	var paths = []string{name, name + "/"}
	for _, path := range paths {
		log.Debug(path)
		s.Run(path, func() {
			entries, err := s.az.ReadDir(internal.ReadDirOptions{Name: path})
			s.assert.Nil(err)
			s.assert.EqualValues(1, len(entries))
		})
	}
}

func (s *datalakeTestSuite) TestReadDirHierarchy() {
	defer s.cleanupTest()
	// Setup
	base := generateDirectoryName()
	s.setupHierarchy(base)

	// ReadDir only reads the first level of the hierarchy
	//Using listblob api lists the files before directories so the order is reversed
	entries, err := s.az.ReadDir(internal.ReadDirOptions{Name: base})
	s.assert.Nil(err)
	s.assert.EqualValues(2, len(entries))
	// Check the dir
	s.assert.EqualValues(base+"/c1", entries[1].Path)
	s.assert.EqualValues("c1", entries[1].Name)
	s.assert.True(entries[1].IsDir())
	s.assert.False(entries[1].IsModeDefault())
	// Check the file
	s.assert.EqualValues(base+"/c2", entries[0].Path)
	s.assert.EqualValues("c2", entries[0].Name)
	s.assert.False(entries[0].IsDir())
	s.assert.False(entries[0].IsModeDefault())
}

func (s *datalakeTestSuite) TestReadDirRoot() {
	defer s.cleanupTest()
	// Setup
	base := generateDirectoryName()
	s.setupHierarchy(base)

	// Testing dir and dir/
	var paths = []string{"", "/"}
	for _, path := range paths {
		log.Debug(path)
		s.Run(path, func() {
			// ReadDir only reads the first level of the hierarchy
			entries, err := s.az.ReadDir(internal.ReadDirOptions{Name: path})
			s.assert.Nil(err)
			s.assert.EqualValues(3, len(entries))
			//Listblob api lists files before directories so the order is reversed
			// Check the base dir
			s.assert.EqualValues(base, entries[1].Path)
			s.assert.EqualValues(base, entries[1].Name)
			s.assert.True(entries[1].IsDir())
			s.assert.False(entries[1].IsModeDefault())
			// Check the baseb dir
			s.assert.EqualValues(base+"b", entries[2].Path)
			s.assert.EqualValues(base+"b", entries[2].Name)
			s.assert.True(entries[2].IsDir())
			s.assert.False(entries[2].IsModeDefault())
			// Check the basec file
			s.assert.EqualValues(base+"c", entries[0].Path)
			s.assert.EqualValues(base+"c", entries[0].Name)
			s.assert.False(entries[0].IsDir())
			s.assert.False(entries[0].IsModeDefault())
		})
	}
}

func (s *datalakeTestSuite) TestReadDirSubDir() {
	defer s.cleanupTest()
	// Setup
	base := generateDirectoryName()
	s.setupHierarchy(base)

	// ReadDir only reads the first level of the hierarchy
	entries, err := s.az.ReadDir(internal.ReadDirOptions{Name: base + "/c1"})
	s.assert.Nil(err)
	s.assert.EqualValues(1, len(entries))
	// Check the dir
	s.assert.EqualValues(base+"/c1"+"/gc1", entries[0].Path)
	s.assert.EqualValues("gc1", entries[0].Name)
	s.assert.False(entries[0].IsDir())
	s.assert.False(entries[0].IsModeDefault())
}

func (s *datalakeTestSuite) TestReadDirSubDirPrefixPath() {
	defer s.cleanupTest()
	// Setup
	base := generateDirectoryName()
	s.setupHierarchy(base)

	s.az.storage.SetPrefixPath(base)

	// ReadDir only reads the first level of the hierarchy
	entries, err := s.az.ReadDir(internal.ReadDirOptions{Name: "/c1"})
	s.assert.Nil(err)
	s.assert.EqualValues(1, len(entries))
	// Check the dir
	s.assert.EqualValues("c1"+"/gc1", entries[0].Path)
	s.assert.EqualValues("gc1", entries[0].Name)
	s.assert.False(entries[0].IsDir())
	s.assert.False(entries[0].IsModeDefault())
}

func (s *datalakeTestSuite) TestReadDirError() {
	defer s.cleanupTest()
	// Setup
	name := generateDirectoryName()

	entries, err := s.az.ReadDir(internal.ReadDirOptions{Name: name})

	s.assert.Nil(err) // Note: See comment in BlockBlob.List. BlockBlob behaves differently from Datalake
	s.assert.Empty(entries)
	// Directory should not be in the account
	dir := s.containerClient.NewDirectoryClient(name)
	_, err = dir.GetProperties(ctx, nil)
	s.assert.NotNil(err)
}

func (s *datalakeTestSuite) TestReadDirListBlocked() {
	defer s.cleanupTest()
	// Setup
	s.tearDownTestHelper(false) // Don't delete the generated container.

	listBlockedTime := 10
	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  block-list-on-mount-sec: %d\n  fail-unsupported-op: true\n",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container, listBlockedTime)
	s.setupTestHelper(config, s.container, true)

	name := generateDirectoryName()
	s.az.CreateDir(internal.CreateDirOptions{Name: name})
	childName := name + "/" + generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: childName})

	entries, err := s.az.ReadDir(internal.ReadDirOptions{Name: name})
	s.assert.Nil(err)
	s.assert.EqualValues(0, len(entries)) // Since we block the list, it will return an empty list.
}

func (s *datalakeTestSuite) TestRenameDir() {
	defer s.cleanupTest()
	// Test handling "dir" and "dir/"
	var inputs = []struct {
		src string
		dst string
	}{
		{src: generateDirectoryName(), dst: generateDirectoryName()},
		{src: generateDirectoryName() + "/", dst: generateDirectoryName()},
		{src: generateDirectoryName(), dst: generateDirectoryName() + "/"},
		{src: generateDirectoryName() + "/", dst: generateDirectoryName() + "/"},
	}

	for _, input := range inputs {
		s.Run(input.src+"->"+input.dst, func() {
			// Setup
			s.az.CreateDir(internal.CreateDirOptions{Name: input.src})

			err := s.az.RenameDir(internal.RenameDirOptions{Src: input.src, Dst: input.dst})
			s.assert.Nil(err)
			// Src should not be in the account
			dir := s.containerClient.NewDirectoryClient(internal.TruncateDirName(input.src))
			_, err = dir.GetProperties(ctx, nil)
			s.assert.NotNil(err)

			// Dst should be in the account
			dir = s.containerClient.NewDirectoryClient(internal.TruncateDirName(input.dst))
			_, err = dir.GetProperties(ctx, nil)
			s.assert.Nil(err)
		})
	}

}

func (s *datalakeTestSuite) TestRenameDirWithCPKEnabled() {
	defer s.cleanupTest()
	CPKEncryptionKey, CPKEncryptionKeySHA256 := generateCPKInfo()

	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  cpk-enabled: true\n  cpk-encryption-key: %s\n  cpk-encryption-key-sha256: %s\n",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container, CPKEncryptionKey, CPKEncryptionKeySHA256)
	s.setupTestHelper(config, s.container, false)

	datalakeCPKOpt := &file.CPKInfo{
		EncryptionKey:       &CPKEncryptionKey,
		EncryptionKeySHA256: &CPKEncryptionKeySHA256,
		EncryptionAlgorithm: to.Ptr(file.EncryptionAlgorithmTypeAES256),
	}

	// Test handling "dir" and "dir/"
	var inputs = []struct {
		src string
		dst string
	}{
		{src: generateDirectoryName(), dst: generateDirectoryName()},
		{src: generateDirectoryName() + "/", dst: generateDirectoryName()},
		{src: generateDirectoryName(), dst: generateDirectoryName() + "/"},
		{src: generateDirectoryName() + "/", dst: generateDirectoryName() + "/"},
	}

	for _, input := range inputs {
		s.Run(input.src+"->"+input.dst, func() {
			// Setup
			s.az.CreateDir(internal.CreateDirOptions{Name: input.src})

			err := s.az.RenameDir(internal.RenameDirOptions{Src: input.src, Dst: input.dst})
			s.assert.Nil(err)
			// Src should not be in the account
			dir := s.containerClient.NewDirectoryClient(internal.TruncateDirName(input.src))
			_, err = dir.GetProperties(ctx, &directory.GetPropertiesOptions{
				CPKInfo: datalakeCPKOpt,
			})
			s.assert.NotNil(err)

			// Dst should be in the account
			dir = s.containerClient.NewDirectoryClient(internal.TruncateDirName(input.dst))
			_, err = dir.GetProperties(ctx, &directory.GetPropertiesOptions{
				CPKInfo: datalakeCPKOpt,
			})
			s.assert.Nil(err)
		})
	}

}

func (s *datalakeTestSuite) TestRenameDirHierarchy() {
	defer s.cleanupTest()
	// Setup
	baseSrc := generateDirectoryName()
	aSrc, abSrc, acSrc := s.setupHierarchy(baseSrc)
	baseDst := generateDirectoryName()
	aDst, abDst, acDst := generateNestedDirectory(baseDst)

	err := s.az.RenameDir(internal.RenameDirOptions{Src: baseSrc, Dst: baseDst})
	s.assert.Nil(err)

	// Source
	// aSrc paths should be deleted
	for p := aSrc.Front(); p != nil; p = p.Next() {
		_, err = s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.NotNil(err)
	}
	abSrc.PushBackList(acSrc) // abSrc and acSrc paths should exist
	for p := abSrc.Front(); p != nil; p = p.Next() {
		_, err = s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.Nil(err)
	}
	// Destination
	// aDst paths should exist
	for p := aDst.Front(); p != nil; p = p.Next() {
		_, err = s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.Nil(err)
	}
	abDst.PushBackList(acDst) // abDst and acDst paths should not exist
	for p := abDst.Front(); p != nil; p = p.Next() {
		_, err = s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.NotNil(err)
	}
}

func (s *datalakeTestSuite) TestRenameDirSubDirPrefixPath() {
	defer s.cleanupTest()
	// Setup
	baseSrc := generateDirectoryName()
	aSrc, abSrc, acSrc := s.setupHierarchy(baseSrc)
	baseDst := generateDirectoryName()

	s.az.storage.SetPrefixPath(baseSrc)

	err := s.az.RenameDir(internal.RenameDirOptions{Src: "c1", Dst: baseDst})
	s.assert.Nil(err)

	// Source
	// aSrc paths under c1 should be deleted
	for p := aSrc.Front(); p != nil; p = p.Next() {
		path := p.Value.(string)
		_, err = s.containerClient.NewDirectoryClient(path).GetProperties(ctx, nil)
		if strings.HasPrefix(path, baseSrc+"/c1") {
			s.assert.NotNil(err)
		} else {
			s.assert.Nil(err)
		}
	}
	abSrc.PushBackList(acSrc) // abSrc and acSrc paths should exist
	for p := abSrc.Front(); p != nil; p = p.Next() {
		_, err = s.containerClient.NewDirectoryClient(p.Value.(string)).GetProperties(ctx, nil)
		s.assert.Nil(err)
	}
	// Destination
	// aDst paths should exist -> aDst and aDst/gc1
	_, err = s.containerClient.NewDirectoryClient(baseSrc+"/"+baseDst).GetProperties(ctx, nil)
	s.assert.Nil(err)
	_, err = s.containerClient.NewDirectoryClient(baseSrc+"/"+baseDst+"/gc1").GetProperties(ctx, nil)
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestRenameDirError() {
	defer s.cleanupTest()
	// Setup
	src := generateDirectoryName()
	dst := generateDirectoryName()

	err := s.az.RenameDir(internal.RenameDirOptions{Src: src, Dst: dst})

	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)
	// Neither directory should be in the account
	dir := s.containerClient.NewDirectoryClient(src)
	_, err = dir.GetProperties(ctx, nil)
	s.assert.NotNil(err)
	dir = s.containerClient.NewDirectoryClient(dst)
	_, err = dir.GetProperties(ctx, nil)
	s.assert.NotNil(err)
}

func (s *datalakeTestSuite) TestCreateFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()

	h, err := s.az.CreateFile(internal.CreateFileOptions{Name: name})

	s.assert.Nil(err)
	s.assert.NotNil(h)
	s.assert.EqualValues(name, h.Path)
	s.assert.EqualValues(0, h.Size)
	// File should be in the account
	file := s.containerClient.NewDirectoryClient(name)
	props, err := file.GetProperties(ctx, nil)
	s.assert.Nil(err)
	s.assert.NotNil(props)
	s.assert.Empty(props.Metadata)
}

func (s *datalakeTestSuite) TestWriteSmallFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	dataLen := len(data)
	_, err := s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	output := make([]byte, len(data))
	f, _ = os.Open(f.Name())
	len, err := f.Read(output)
	s.assert.Nil(err)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(testData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestOverwriteSmallFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test-replace-data"
	data := []byte(testData)
	dataLen := len(data)
	_, err := s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())
	newTestData := []byte("newdata")
	_, err = s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 5, Data: newTestData})
	s.assert.Nil(err)

	currentData := []byte("test-newdata-data")
	output := make([]byte, len(currentData))

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	f, _ = os.Open(f.Name())
	len, err := f.Read(output)
	s.assert.Nil(err)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(currentData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestOverwriteAndAppendToSmallFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test-data"
	data := []byte(testData)

	_, err := s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())
	newTestData := []byte("newdata")
	_, err = s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 5, Data: newTestData})
	s.assert.Nil(err)

	currentData := []byte("test-newdata")
	dataLen := len(currentData)
	output := make([]byte, dataLen)

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	f, _ = os.Open(f.Name())
	len, err := f.Read(output)
	s.assert.Nil(err)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(currentData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestAppendOffsetLargerThanSmallFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test-data"
	data := []byte(testData)

	_, err := s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())
	newTestData := []byte("newdata")
	_, err = s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 12, Data: newTestData})
	s.assert.Nil(err)

	currentData := []byte("test-data\x00\x00\x00newdata")
	dataLen := len(currentData)
	output := make([]byte, dataLen)

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	f, _ = os.Open(f.Name())
	len, err := f.Read(output)
	s.assert.Nil(err)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(currentData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestAppendToSmallFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test-data"
	data := []byte(testData)

	_, err := s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())
	newTestData := []byte("-newdata")
	_, err = s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 9, Data: newTestData})
	s.assert.Nil(err)

	currentData := []byte("test-data-newdata")
	dataLen := len(currentData)
	output := make([]byte, dataLen)

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	f, _ = os.Open(f.Name())
	len, err := f.Read(output)
	s.assert.Nil(err)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(currentData, output)
	f.Close()
}

// This test is a regular blob (without blocks) and we're adding data that will cause it to create blocks
func (s *datalakeTestSuite) TestAppendBlocksToSmallFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test-data"
	data := []byte(testData)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 9 Bytes
	err := uploadReaderAtToBlockBlob(
		ctx, bytes.NewReader(data),
		int64(len(data)),
		9,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name),
		&blockblob.UploadBufferOptions{
			BlockSize: 8,
		})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())
	newTestData := []byte("-newdata-newdata-newdata")
	_, err = s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 9, Data: newTestData})
	s.assert.Nil(err)

	currentData := []byte("test-data-newdata-newdata-newdata")
	dataLen := len(currentData)
	output := make([]byte, dataLen)

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	f, _ = os.Open(f.Name())
	len, err := f.Read(output)
	s.assert.Nil(err)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(currentData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestOverwriteBlocks() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "testdatates1dat1tes2dat2tes3dat3tes4dat4"
	data := []byte(testData)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(
		ctx,
		bytes.NewReader(data),
		int64(len(data)),
		4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name),
		&blockblob.UploadBufferOptions{
			BlockSize: 4,
		})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())
	newTestData := []byte("cake")
	_, err = s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 16, Data: newTestData})
	s.assert.Nil(err)

	currentData := []byte("testdatates1dat1cakedat2tes3dat3tes4dat4")
	dataLen := len(currentData)
	output := make([]byte, dataLen)

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	f, _ = os.Open(f.Name())
	len, err := f.Read(output)
	s.assert.Nil(err)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(currentData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestOverwriteAndAppendBlocks() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "testdatates1dat1tes2dat2tes3dat3tes4dat4"
	data := []byte(testData)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(
		ctx,
		bytes.NewReader(data),
		int64(len(data)),
		4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name),
		&blockblob.UploadBufferOptions{
			BlockSize: 4,
		})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())
	newTestData := []byte("43211234cake")
	_, err = s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 32, Data: newTestData})
	s.assert.Nil(err)

	currentData := []byte("testdatates1dat1tes2dat2tes3dat343211234cake")
	dataLen := len(currentData)
	output := make([]byte, dataLen)

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	f, _ = os.Open(f.Name())
	len, _ := f.Read(output)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(currentData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestAppendBlocks() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "testdatates1dat1tes2dat2tes3dat3tes4dat4"
	data := []byte(testData)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx,
		bytes.NewReader(data),
		int64(len(data)),
		4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name),
		&blockblob.UploadBufferOptions{
			BlockSize: 4,
		})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())
	newTestData := []byte("43211234cake")
	_, err = s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: newTestData})
	s.assert.Nil(err)

	currentData := []byte("43211234cakedat1tes2dat2tes3dat3tes4dat4")
	dataLen := len(currentData)
	output := make([]byte, dataLen)

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	f, _ = os.Open(f.Name())
	len, _ := f.Read(output)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(currentData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestAppendOffsetLargerThanSize() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "testdatates1dat1tes2dat2tes3dat3tes4dat4"
	data := []byte(testData)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx,
		bytes.NewReader(data),
		int64(len(data)),
		4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name),
		&blockblob.UploadBufferOptions{
			BlockSize: 4,
		})
	s.assert.Nil(err)
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())
	newTestData := []byte("43211234cake")
	_, err = s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 45, Data: newTestData})
	s.assert.Nil(err)

	currentData := []byte("testdatates1dat1tes2dat2tes3dat3tes4dat4\x00\x00\x00\x00\x0043211234cake")
	dataLen := len(currentData)
	output := make([]byte, dataLen)

	err = s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	f, _ = os.Open(f.Name())
	len, _ := f.Read(output)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(currentData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestOpenFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})

	h, err := s.az.OpenFile(internal.OpenFileOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(h)
	s.assert.EqualValues(name, h.Path)
	s.assert.EqualValues(0, h.Size)
}

func (s *datalakeTestSuite) TestOpenFileError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()

	h, err := s.az.OpenFile(internal.OpenFileOptions{Name: name})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)
	s.assert.Nil(h)
}

func (s *datalakeTestSuite) TestOpenFileSize() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	size := 10
	s.az.CreateFile(internal.CreateFileOptions{Name: name})
	s.az.TruncateFile(internal.TruncateFileOptions{Name: name, Size: int64(size)})

	h, err := s.az.OpenFile(internal.OpenFileOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(h)
	s.assert.EqualValues(name, h.Path)
	s.assert.EqualValues(size, h.Size)
}

func (s *datalakeTestSuite) TestCloseFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})

	// This method does nothing.
	err := s.az.CloseFile(internal.CloseFileOptions{Handle: h})
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestCloseFileFakeHandle() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h := handlemap.NewHandle(name)

	// This method does nothing.
	err := s.az.CloseFile(internal.CloseFileOptions{Handle: h})
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestDeleteFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})

	err := s.az.DeleteFile(internal.DeleteFileOptions{Name: name})
	s.assert.Nil(err)

	// File should not be in the account
	file := s.containerClient.NewDirectoryClient(name)
	_, err = file.GetProperties(ctx, nil)
	s.assert.NotNil(err)
}

func (s *datalakeTestSuite) TestDeleteFileError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()

	err := s.az.DeleteFile(internal.DeleteFileOptions{Name: name})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)

	// File should not be in the account
	file := s.containerClient.NewDirectoryClient(name)
	_, err = file.GetProperties(ctx, nil)
	s.assert.NotNil(err)
}

func (s *datalakeTestSuite) TestRenameFile() {
	defer s.cleanupTest()
	// Setup
	src := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: src})
	dst := generateFileName()

	err := s.az.RenameFile(internal.RenameFileOptions{Src: src, Dst: dst})
	s.assert.Nil(err)

	// Src should not be in the account
	source := s.containerClient.NewDirectoryClient(src)
	_, err = source.GetProperties(ctx, nil)
	s.assert.NotNil(err)
	// Dst should be in the account
	destination := s.containerClient.NewDirectoryClient(dst)
	_, err = destination.GetProperties(ctx, nil)
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestRenameFileWithCPKenabled() {
	defer s.cleanupTest()
	CPKEncryptionKey, CPKEncryptionKeySHA256 := generateCPKInfo()

	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  cpk-enabled: true\n  cpk-encryption-key: %s\n  cpk-encryption-key-sha256: %s\n",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container, CPKEncryptionKey, CPKEncryptionKeySHA256)
	s.setupTestHelper(config, s.container, false)

	datalakeCPKOpt := &file.CPKInfo{
		EncryptionKey:       &CPKEncryptionKey,
		EncryptionKeySHA256: &CPKEncryptionKeySHA256,
		EncryptionAlgorithm: to.Ptr(file.EncryptionAlgorithmTypeAES256),
	}

	src := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: src})
	dst := generateFileName()

	testData := "test data"
	data := []byte(testData)

	err := uploadReaderAtToBlockBlob(
		ctx, bytes.NewReader(data),
		int64(len(data)),
		100,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(src),
		&blockblob.UploadBufferOptions{
			CPKInfo: &blob.CPKInfo{
				EncryptionKey:       &CPKEncryptionKey,
				EncryptionKeySHA256: &CPKEncryptionKeySHA256,
				EncryptionAlgorithm: to.Ptr(blob.EncryptionAlgorithmTypeAES256),
			},
		})
	s.assert.Nil(err)

	err = s.az.RenameFile(internal.RenameFileOptions{Src: src, Dst: dst})
	s.assert.Nil(err)

	// Src should not be in the account
	source := s.containerClient.NewDirectoryClient(src)
	_, err = source.GetProperties(ctx, &file.GetPropertiesOptions{
		CPKInfo: datalakeCPKOpt,
	})
	s.assert.NotNil(err)
	// Dst should be in the account
	destination := s.containerClient.NewDirectoryClient(dst)
	_, err = destination.GetProperties(ctx, &file.GetPropertiesOptions{
		CPKInfo: datalakeCPKOpt,
	})
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestRenameFileMetadataConservation() {
	defer s.cleanupTest()
	// Setup
	src := generateFileName()
	source := s.containerClient.NewFileClient(src)
	s.az.CreateFile(internal.CreateFileOptions{Name: src})
	// Add srcMeta to source
	srcMeta := make(map[string]*string)
	srcMeta["foo"] = to.Ptr("bar")
	source.SetMetadata(ctx, srcMeta, nil)
	dst := generateFileName()

	err := s.az.RenameFile(internal.RenameFileOptions{Src: src, Dst: dst})
	s.assert.Nil(err)

	// Src should not be in the account
	_, err = source.GetProperties(ctx, nil)
	s.assert.NotNil(err)
	// Dst should be in the account
	destination := s.containerClient.NewFileClient(dst)
	props, err := destination.GetProperties(ctx, nil)
	s.assert.Nil(err)
	// Dst should have metadata
	s.assert.True(checkMetadata(props.Metadata, "foo", "bar"))
}

func (s *datalakeTestSuite) TestRenameFileError() {
	defer s.cleanupTest()
	// Setup
	src := generateFileName()
	dst := generateFileName()

	err := s.az.RenameFile(internal.RenameFileOptions{Src: src, Dst: dst})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)

	// Src and destination should not be in the account
	source := s.containerClient.NewDirectoryClient(src)
	_, err = source.GetProperties(ctx, nil)
	s.assert.NotNil(err)
	destination := s.containerClient.NewDirectoryClient(dst)
	_, err = destination.GetProperties(ctx, nil)
	s.assert.NotNil(err)
}

func (s *datalakeTestSuite) TestReadFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	h, _ = s.az.OpenFile(internal.OpenFileOptions{Name: name})

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	s.assert.EqualValues(testData, output)
}

func (s *datalakeTestSuite) TestReadFileError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h := handlemap.NewHandle(name)

	_, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)
}

func (s *datalakeTestSuite) TestReadInBuffer() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	h, _ = s.az.OpenFile(internal.OpenFileOptions{Name: name})

	output := make([]byte, 5)
	len, err := s.az.ReadInBuffer(internal.ReadInBufferOptions{Handle: h, Offset: 0, Data: output})
	s.assert.Nil(err)
	s.assert.EqualValues(5, len)
	s.assert.EqualValues(testData[:5], output)
}

func (s *datalakeTestSuite) TestReadInBufferWithoutHandle() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, err := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(h)

	testData := "test data"
	data := []byte(testData)
	n, err := s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	s.assert.Nil(err)
	s.assert.Equal(n, len(data))

	output := make([]byte, 5)
	len, err := s.az.ReadInBuffer(internal.ReadInBufferOptions{Offset: 0, Data: output, Path: name, Size: (int64)(len(data))})
	s.assert.Nil(err)
	s.assert.EqualValues(5, len)
	s.assert.EqualValues(testData[:5], output)
}

func (s *datalakeTestSuite) TestReadInBufferEmptyPath() {
	defer s.cleanupTest()

	output := make([]byte, 5)
	len, err := s.az.ReadInBuffer(internal.ReadInBufferOptions{Offset: 0, Data: output, Size: 5})
	s.assert.NotNil(err)
	s.assert.EqualValues(0, len)
	s.assert.Equal(err.Error(), "path not given for download")
}

func (suite *datalakeTestSuite) TestReadInBufferWithETAG() {
	defer suite.cleanupTest()
	// Setup
	name := generateFileName()
	fileHandle, _ := suite.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	suite.az.WriteFile(internal.WriteFileOptions{Handle: fileHandle, Offset: 0, Data: data})
	fileHandle, _ = suite.az.OpenFile(internal.OpenFileOptions{Name: name})

	output := make([]byte, 5)
	var etag string
	len, err := suite.az.ReadInBuffer(internal.ReadInBufferOptions{Handle: fileHandle, Offset: 0, Data: output, Etag: &etag})
	suite.assert.Nil(err)
	suite.assert.NotEqual(etag, "")
	suite.assert.EqualValues(5, len)
	suite.assert.EqualValues(testData[:5], output)
	_ = suite.az.CloseFile(internal.CloseFileOptions{Handle: fileHandle})
}

func (s *datalakeTestSuite) TestReadInBufferLargeBuffer() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	h, _ = s.az.OpenFile(internal.OpenFileOptions{Name: name})

	output := make([]byte, 1000) // Testing that passing in a super large buffer will still work
	len, err := s.az.ReadInBuffer(internal.ReadInBufferOptions{Handle: h, Offset: 0, Data: output})
	s.assert.Nil(err)
	s.assert.EqualValues(h.Size, len)
	s.assert.EqualValues(testData, output[:h.Size])
}

func (s *datalakeTestSuite) TestReadInBufferEmpty() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})

	output := make([]byte, 10)
	len, err := s.az.ReadInBuffer(internal.ReadInBufferOptions{Handle: h, Offset: 0, Data: output})
	s.assert.Nil(err)
	s.assert.EqualValues(0, len)
}

func (s *datalakeTestSuite) TestReadInBufferBadRange() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h := handlemap.NewHandle(name)
	h.Size = 10

	_, err := s.az.ReadInBuffer(internal.ReadInBufferOptions{Handle: h, Offset: 20, Data: make([]byte, 2)})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ERANGE, err)
}

func (s *datalakeTestSuite) TestReadInBufferError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h := handlemap.NewHandle(name)
	h.Size = 10

	_, err := s.az.ReadInBuffer(internal.ReadInBufferOptions{Handle: h, Offset: 0, Data: make([]byte, 2)})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)
}

func (s *datalakeTestSuite) TestWriteFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})

	testData := "test data"
	data := []byte(testData)
	count, err := s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	s.assert.Nil(err)
	s.assert.EqualValues(len(data), count)

	// Blob should have updated data
	fileClient := s.containerClient.NewFileClient(name)
	resp, err := fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(len(data))},
	})
	s.assert.Nil(err)
	output, _ := io.ReadAll(resp.Body)
	s.assert.EqualValues(testData, output)
}

func (s *datalakeTestSuite) TestTruncateSmallFileSmaller() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	truncatedLength := 5
	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})

	err := s.az.TruncateFile(internal.TruncateFileOptions{Name: name, Size: int64(truncatedLength)})
	s.assert.Nil(err)

	// Blob should have updated data
	fileClient := s.containerClient.NewFileClient(name)
	resp, err := fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(truncatedLength)},
	})
	// 0, int64(truncatedLength))
	s.assert.Nil(err)
	s.assert.EqualValues(truncatedLength, *resp.ContentLength)
	output, _ := io.ReadAll(resp.Body)
	s.assert.EqualValues(testData[:truncatedLength], output)
}

func (s *datalakeTestSuite) TestTruncateChunkedFileSmaller() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	truncatedLength := 5
	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx, bytes.NewReader(data), int64(len(data)), 4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name), &blockblob.UploadBufferOptions{
			BlockSize: 4,
		})
	s.assert.Nil(err)

	err = s.az.TruncateFile(internal.TruncateFileOptions{Name: name, Size: int64(truncatedLength)})
	s.assert.Nil(err)

	// Blob should have updated data
	fileClient := s.containerClient.NewFileClient(name)
	resp, err := fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(truncatedLength)},
	})
	s.assert.Nil(err)
	s.assert.EqualValues(truncatedLength, *resp.ContentLength)
	output, _ := io.ReadAll(resp.Body)
	s.assert.EqualValues(testData[:truncatedLength], output)
}

func (s *datalakeTestSuite) TestTruncateSmallFileEqual() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	truncatedLength := 9
	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})

	err := s.az.TruncateFile(internal.TruncateFileOptions{Name: name, Size: int64(truncatedLength)})
	s.assert.Nil(err)

	// Blob should have updated data
	fileClient := s.containerClient.NewFileClient(name)
	resp, err := fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(truncatedLength)},
	})
	s.assert.Nil(err)
	s.assert.EqualValues(truncatedLength, *resp.ContentLength)
	output, _ := io.ReadAll(resp.Body)
	s.assert.EqualValues(testData, output)
}

func (s *datalakeTestSuite) TestTruncateChunkedFileEqual() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	truncatedLength := 9
	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx, bytes.NewReader(data), int64(len(data)), 4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name), &blockblob.UploadBufferOptions{
			BlockSize: 4,
		})
	s.assert.Nil(err)

	err = s.az.TruncateFile(internal.TruncateFileOptions{Name: name, Size: int64(truncatedLength)})
	s.assert.Nil(err)

	// Blob should have updated data
	fileClient := s.containerClient.NewFileClient(name)
	resp, err := fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(truncatedLength)},
	})
	s.assert.Nil(err)
	s.assert.EqualValues(truncatedLength, *resp.ContentLength)
	output, _ := io.ReadAll(resp.Body)
	s.assert.EqualValues(testData, output)
}

func (s *datalakeTestSuite) TestTruncateSmallFileBigger() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	truncatedLength := 15
	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})

	err := s.az.TruncateFile(internal.TruncateFileOptions{Name: name, Size: int64(truncatedLength)})
	s.assert.Nil(err)

	// Blob should have updated data
	fileClient := s.containerClient.NewFileClient(name)
	resp, err := fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(truncatedLength)},
	})
	s.assert.Nil(err)
	s.assert.EqualValues(truncatedLength, *resp.ContentLength)
	output, _ := io.ReadAll(resp.Body)
	s.assert.EqualValues(testData, output[:len(data)])
}

func (s *datalakeTestSuite) TestTruncateChunkedFileBigger() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	truncatedLength := 15
	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx, bytes.NewReader(data), int64(len(data)), 4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name), &blockblob.UploadBufferOptions{
			BlockSize: 4,
		})
	s.assert.Nil(err)

	s.az.TruncateFile(internal.TruncateFileOptions{Name: name, Size: int64(truncatedLength)})
	s.assert.Nil(err)

	// Blob should have updated data
	fileClient := s.containerClient.NewFileClient(name)
	resp, err := fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(truncatedLength)},
	})
	s.assert.Nil(err)
	s.assert.EqualValues(truncatedLength, *resp.ContentLength)
	output, _ := io.ReadAll(resp.Body)
	s.assert.EqualValues(testData, output[:len(data)])
}

func (s *datalakeTestSuite) TestTruncateFileError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()

	err := s.az.TruncateFile(internal.TruncateFileOptions{Name: name})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)
}

func (s *datalakeTestSuite) TestCopyToFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	dataLen := len(data)
	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())

	err := s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.Nil(err)

	output := make([]byte, len(data))
	f, _ = os.Open(f.Name())
	len, err := f.Read(output)
	s.assert.Nil(err)
	s.assert.EqualValues(dataLen, len)
	s.assert.EqualValues(testData, output)
	f.Close()
}

func (s *datalakeTestSuite) TestCopyToFileError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	f, _ := os.CreateTemp("", name+".tmp")
	defer os.Remove(f.Name())

	err := s.az.CopyToFile(internal.CopyToFileOptions{Name: name, File: f})
	s.assert.NotNil(err)
}

func (s *datalakeTestSuite) TestCopyFromFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	homeDir, _ := os.UserHomeDir()
	f, _ := os.CreateTemp(homeDir, name+".tmp")
	defer os.Remove(f.Name())
	f.Write(data)

	err := s.az.CopyFromFile(internal.CopyFromFileOptions{Name: name, File: f})

	s.assert.Nil(err)

	// Blob should have updated data
	fileClient := s.containerClient.NewFileClient(name)
	resp, err := fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(len(data))},
	})
	s.assert.Nil(err)
	output, _ := io.ReadAll(resp.Body)
	s.assert.EqualValues(testData, output)
}

func (s *datalakeTestSuite) TestCreateLink() {
	defer s.cleanupTest()
	// Setup
	target := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: target})
	name := generateFileName()

	err := s.az.CreateLink(internal.CreateLinkOptions{Name: name, Target: target})
	s.assert.Nil(err)

	// Link should be in the account
	link := s.containerClient.NewFileClient(name)
	props, err := link.GetProperties(ctx, nil)
	s.assert.Nil(err)
	s.assert.NotNil(props)
	s.assert.NotEmpty(props.Metadata)
	s.assert.True(checkMetadata(props.Metadata, symlinkKey, "true"))
	resp, err := link.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: *props.ContentLength},
	})
	s.assert.Nil(err)
	data, _ := io.ReadAll(resp.Body)
	s.assert.EqualValues(target, data)
}

func (s *datalakeTestSuite) TestReadLink() {
	defer s.cleanupTest()
	// Setup
	target := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: target})
	name := generateFileName()
	s.az.CreateLink(internal.CreateLinkOptions{Name: name, Target: target})

	read, err := s.az.ReadLink(internal.ReadLinkOptions{Name: name})
	s.assert.Nil(err)
	s.assert.EqualValues(target, read)
}

func (s *datalakeTestSuite) TestReadLinkError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()

	_, err := s.az.ReadLink(internal.ReadLinkOptions{Name: name})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)
}

func (s *datalakeTestSuite) TestGetAttrDir() {
	defer s.cleanupTest()
	// Setup
	name := generateDirectoryName()
	s.az.CreateDir(internal.CreateDirOptions{Name: name})

	props, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(props)
	s.assert.True(props.IsDir())
}

func (s *datalakeTestSuite) TestGetAttrFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})

	props, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(props)
	s.assert.False(props.IsDir())
	s.assert.False(props.IsSymlink())
}

func (s *datalakeTestSuite) TestGetAttrLink() {
	defer s.cleanupTest()
	// Setup
	target := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: target})
	name := generateFileName()
	s.az.CreateLink(internal.CreateLinkOptions{Name: name, Target: target})

	props, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(props)
	s.assert.True(props.IsSymlink())
	s.assert.NotEmpty(props.Metadata)
	s.assert.True(checkMetadata(props.Metadata, symlinkKey, "true"))
}

func (s *datalakeTestSuite) TestGetAttrFileSize() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})

	props, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(props)
	s.assert.False(props.IsDir())
	s.assert.False(props.IsSymlink())
	s.assert.EqualValues(len(testData), props.Size)
}

func (s *datalakeTestSuite) TestGetAttrFileTime() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)
	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})

	before, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(before.Mtime)

	time.Sleep(time.Second * 3) // Wait 3 seconds and then modify the file again

	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	time.Sleep(time.Second * 1)

	after, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(after.Mtime)

	s.assert.True(after.Mtime.After(before.Mtime))
}

func (s *datalakeTestSuite) TestGetAttrError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()

	_, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)
}

func (s *datalakeTestSuite) TestChmod() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})

	err := s.az.Chmod(internal.ChmodOptions{Name: name, Mode: 0666})
	s.assert.Nil(err)

	// File's ACL info should have changed
	file := s.containerClient.NewFileClient(name)
	acl, err := file.GetAccessControl(ctx, nil)
	s.assert.Nil(err)
	s.assert.NotNil(acl.ACL)
	s.assert.EqualValues("user::rw-,group::rw-,other::rw-", *acl.ACL)
}

func (s *datalakeTestSuite) TestChmodError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()

	err := s.az.Chmod(internal.ChmodOptions{Name: name, Mode: 0666})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOENT, err)
}

// If support for chown or chmod are ever added to blob, add tests for error cases and modify the following tests.
func (s *datalakeTestSuite) TestChown() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})

	err := s.az.Chown(internal.ChownOptions{Name: name, Owner: 6, Group: 5})
	s.assert.NotNil(err)
	s.assert.EqualValues(syscall.ENOTSUP, err)
}

func (s *datalakeTestSuite) TestChownIgnore() {
	defer s.cleanupTest()
	// Setup
	s.tearDownTestHelper(false) // Don't delete the generated container.

	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  fail-unsupported-op: false\n",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container)
	s.setupTestHelper(config, s.container, true)
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})

	err := s.az.Chown(internal.ChownOptions{Name: name, Owner: 6, Group: 5})
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestGetFileBlockOffsetsSmallFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "testdatates1dat1tes2dat2tes3dat3tes4dat4"
	data := []byte(testData)

	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})

	// GetFileBlockOffsets
	offsetList, err := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	s.assert.Nil(err)
	s.assert.Len(offsetList.BlockList, 0)
	s.assert.True(offsetList.SmallFile())
	s.assert.EqualValues(0, offsetList.BlockIdLength)
}

func (s *datalakeTestSuite) TestGetFileBlockOffsetsChunkedFile() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "testdatates1dat1tes2dat2tes3dat3tes4dat4"
	data := []byte(testData)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(
		ctx, bytes.NewReader(data),
		int64(len(data)),
		4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name),
		&blockblob.UploadBufferOptions{
			BlockSize: 4,
		})
	s.assert.Nil(err)

	// GetFileBlockOffsets
	offsetList, err := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	s.assert.Nil(err)
	s.assert.Len(offsetList.BlockList, 10)
	s.assert.Zero(offsetList.Flags)
	s.assert.EqualValues(16, offsetList.BlockIdLength)
}

func (s *datalakeTestSuite) TestGetFileBlockOffsetsError() {
	defer s.cleanupTest()
	// Setup
	name := generateFileName()

	// GetFileBlockOffsets
	_, err := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	s.assert.NotNil(err)
}

func (s *datalakeTestSuite) TestCustomEndpoint() {
	defer s.cleanupTest()
	dfsEndpoint := "https://mycustom.endpoint"

	blobEndpoint := transformAccountEndpoint(dfsEndpoint)
	s.assert.EqualValues(dfsEndpoint, blobEndpoint)
}

func (s *datalakeTestSuite) TestFlushFileEmptyFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})

	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(16*MB), h)
	h.CacheObj.BlockOffsetList = bol

	err := s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	s.assert.EqualValues("", output)
}

func (s *datalakeTestSuite) TestFlushFileChunkedFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	data := make([]byte, 16*MB)
	rand.Read(data)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx, bytes.NewReader(data), int64(len(data)), 4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name), &blockblob.UploadBufferOptions{
			BlockSize: 4 * MB,
		})
	s.assert.Nil(err)
	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(16*MB), h)
	h.CacheObj.BlockOffsetList = bol

	err = s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	s.assert.EqualValues(data, output)
}

func (s *datalakeTestSuite) TestFlushFileUpdateChunkedFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	blockSize := 4 * MB
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	data := make([]byte, 16*MB)
	rand.Read(data)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx, bytes.NewReader(data), int64(len(data)), 4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name), &blockblob.UploadBufferOptions{
			BlockSize: int64(blockSize),
		})
	s.assert.Nil(err)
	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(16*MB), h)
	h.CacheObj.BlockOffsetList = bol

	updatedBlock := make([]byte, 2*MB)
	rand.Read(updatedBlock)
	h.CacheObj.BlockOffsetList.BlockList[1].Data = make([]byte, blockSize)
	s.az.storage.ReadInBuffer(name, int64(blockSize), int64(blockSize), h.CacheObj.BlockOffsetList.BlockList[1].Data, nil)
	copy(h.CacheObj.BlockOffsetList.BlockList[1].Data[MB:2*MB+MB], updatedBlock)
	h.CacheObj.BlockOffsetList.BlockList[1].Flags.Set(common.DirtyBlock)

	err = s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	s.assert.NotEqualValues(data, output)
	s.assert.EqualValues(data[:5*MB], output[:5*MB])
	s.assert.EqualValues(updatedBlock, output[5*MB:5*MB+2*MB])
	s.assert.EqualValues(data[7*MB:], output[7*MB:])
}

func (s *datalakeTestSuite) TestFlushFileTruncateUpdateChunkedFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	blockSize := 4 * MB
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	data := make([]byte, 16*MB)
	rand.Read(data)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx, bytes.NewReader(data), int64(len(data)), 4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name), &blockblob.UploadBufferOptions{
			BlockSize: int64(blockSize),
		})
	s.assert.Nil(err)
	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(16*MB), h)
	h.CacheObj.BlockOffsetList = bol

	// truncate block
	h.CacheObj.BlockOffsetList.BlockList[1].Data = make([]byte, blockSize/2)
	h.CacheObj.BlockOffsetList.BlockList[1].EndIndex = int64(blockSize + blockSize/2)
	s.az.storage.ReadInBuffer(name, int64(blockSize), int64(blockSize)/2, h.CacheObj.BlockOffsetList.BlockList[1].Data, nil)
	h.CacheObj.BlockOffsetList.BlockList[1].Flags.Set(common.DirtyBlock)

	// remove 2 blocks
	h.CacheObj.BlockOffsetList.BlockList = h.CacheObj.BlockOffsetList.BlockList[:2]

	err = s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	s.assert.NotEqualValues(data, output)
	s.assert.EqualValues(data[:6*MB], output[:6*MB])
}

func (s *datalakeTestSuite) TestFlushFileAppendBlocksEmptyFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	blockSize := 2 * MB
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})

	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(12*MB), h)
	h.CacheObj.BlockOffsetList = bol
	h.CacheObj.BlockIdLength = 16

	data1 := make([]byte, blockSize)
	rand.Read(data1)
	blk1 := &common.Block{
		StartIndex: 0,
		EndIndex:   int64(blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
		Data:       data1,
	}
	blk1.Flags.Set(common.DirtyBlock)

	data2 := make([]byte, blockSize)
	rand.Read(data2)
	blk2 := &common.Block{
		StartIndex: int64(blockSize),
		EndIndex:   2 * int64(blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
		Data:       data2,
	}
	blk2.Flags.Set(common.DirtyBlock)

	data3 := make([]byte, blockSize)
	rand.Read(data3)
	blk3 := &common.Block{
		StartIndex: 2 * int64(blockSize),
		EndIndex:   3 * int64(blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
		Data:       data3,
	}
	blk3.Flags.Set(common.DirtyBlock)
	h.CacheObj.BlockOffsetList.BlockList = append(h.CacheObj.BlockOffsetList.BlockList, blk1, blk2, blk3)
	bol.Flags.Clear(common.SmallFile)

	err := s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	s.assert.EqualValues(blk1.Data, output[0:blockSize])
	s.assert.EqualValues(blk2.Data, output[blockSize:2*blockSize])
	s.assert.EqualValues(blk3.Data, output[2*blockSize:3*blockSize])
}

func (s *datalakeTestSuite) TestFlushFileAppendBlocksChunkedFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	blockSize := 2 * MB
	fileSize := 16 * MB
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	data := make([]byte, fileSize)
	rand.Read(data)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx, bytes.NewReader(data), int64(len(data)), 4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name), &blockblob.UploadBufferOptions{
			BlockSize: int64(blockSize),
		})
	s.assert.Nil(err)
	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(16*MB), h)
	h.CacheObj.BlockOffsetList = bol
	h.CacheObj.BlockIdLength = 16

	data1 := make([]byte, blockSize)
	rand.Read(data1)
	blk1 := &common.Block{
		StartIndex: int64(fileSize),
		EndIndex:   int64(fileSize + blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
		Data:       data1,
	}
	blk1.Flags.Set(common.DirtyBlock)

	data2 := make([]byte, blockSize)
	rand.Read(data2)
	blk2 := &common.Block{
		StartIndex: int64(fileSize + blockSize),
		EndIndex:   int64(fileSize + 2*blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
		Data:       data2,
	}
	blk2.Flags.Set(common.DirtyBlock)

	data3 := make([]byte, blockSize)
	rand.Read(data3)
	blk3 := &common.Block{
		StartIndex: int64(fileSize + 2*blockSize),
		EndIndex:   int64(fileSize + 3*blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
		Data:       data3,
	}
	blk3.Flags.Set(common.DirtyBlock)
	h.CacheObj.BlockOffsetList.BlockList = append(h.CacheObj.BlockOffsetList.BlockList, blk1, blk2, blk3)
	bol.Flags.Clear(common.SmallFile)

	err = s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	s.assert.EqualValues(data, output[0:fileSize])
	s.assert.EqualValues(blk1.Data, output[fileSize:fileSize+blockSize])
	s.assert.EqualValues(blk2.Data, output[fileSize+blockSize:fileSize+2*blockSize])
	s.assert.EqualValues(blk3.Data, output[fileSize+2*blockSize:fileSize+3*blockSize])
}

func (s *datalakeTestSuite) TestFlushFileTruncateBlocksEmptyFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	blockSize := 4 * MB
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})

	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(12*MB), h)
	h.CacheObj.BlockOffsetList = bol
	h.CacheObj.BlockIdLength = 16

	blk1 := &common.Block{
		StartIndex: 0,
		EndIndex:   int64(blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk1.Flags.Set(common.TruncatedBlock)
	blk1.Flags.Set(common.DirtyBlock)

	blk2 := &common.Block{
		StartIndex: int64(blockSize),
		EndIndex:   2 * int64(blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk2.Flags.Set(common.TruncatedBlock)
	blk2.Flags.Set(common.DirtyBlock)

	blk3 := &common.Block{
		StartIndex: 2 * int64(blockSize),
		EndIndex:   3 * int64(blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk3.Flags.Set(common.TruncatedBlock)
	blk3.Flags.Set(common.DirtyBlock)
	h.CacheObj.BlockOffsetList.BlockList = append(h.CacheObj.BlockOffsetList.BlockList, blk1, blk2, blk3)
	bol.Flags.Clear(common.SmallFile)

	err := s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	data := make([]byte, 3*blockSize)
	s.assert.EqualValues(data, output)
}

func (s *datalakeTestSuite) TestFlushFileTruncateBlocksChunkedFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	blockSize := 4 * MB
	fileSize := 16 * MB
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	data := make([]byte, fileSize)
	rand.Read(data)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx, bytes.NewReader(data), int64(len(data)), 4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name), &blockblob.UploadBufferOptions{
			BlockSize: int64(blockSize),
		})
	s.assert.Nil(err)
	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(16*MB), h)
	h.CacheObj.BlockOffsetList = bol
	h.CacheObj.BlockIdLength = 16

	blk1 := &common.Block{
		StartIndex: int64(fileSize),
		EndIndex:   int64(fileSize + blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk1.Flags.Set(common.TruncatedBlock)
	blk1.Flags.Set(common.DirtyBlock)

	blk2 := &common.Block{
		StartIndex: int64(fileSize + blockSize),
		EndIndex:   int64(fileSize + 2*blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk2.Flags.Set(common.TruncatedBlock)
	blk2.Flags.Set(common.DirtyBlock)

	blk3 := &common.Block{
		StartIndex: int64(fileSize + 2*blockSize),
		EndIndex:   int64(fileSize + 3*blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk3.Flags.Set(common.TruncatedBlock)
	blk3.Flags.Set(common.DirtyBlock)
	h.CacheObj.BlockOffsetList.BlockList = append(h.CacheObj.BlockOffsetList.BlockList, blk1, blk2, blk3)
	bol.Flags.Clear(common.SmallFile)

	err = s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	s.assert.EqualValues(data, output[:fileSize])
	emptyData := make([]byte, 3*blockSize)
	s.assert.EqualValues(emptyData, output[fileSize:])
}

func (s *datalakeTestSuite) TestFlushFileAppendAndTruncateBlocksEmptyFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	blockSize := 7 * MB
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})

	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(12*MB), h)
	h.CacheObj.BlockOffsetList = bol
	h.CacheObj.BlockIdLength = 16

	data1 := make([]byte, blockSize)
	rand.Read(data1)
	blk1 := &common.Block{
		StartIndex: 0,
		EndIndex:   int64(blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
		Data:       data1,
	}
	blk1.Flags.Set(common.DirtyBlock)

	blk2 := &common.Block{
		StartIndex: int64(blockSize),
		EndIndex:   2 * int64(blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk2.Flags.Set(common.DirtyBlock)
	blk2.Flags.Set(common.TruncatedBlock)

	blk3 := &common.Block{
		StartIndex: 2 * int64(blockSize),
		EndIndex:   3 * int64(blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk3.Flags.Set(common.DirtyBlock)
	blk3.Flags.Set(common.TruncatedBlock)
	h.CacheObj.BlockOffsetList.BlockList = append(h.CacheObj.BlockOffsetList.BlockList, blk1, blk2, blk3)
	bol.Flags.Clear(common.SmallFile)

	err := s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	data := make([]byte, blockSize)
	s.assert.EqualValues(blk1.Data, output[0:blockSize])
	s.assert.EqualValues(data, output[blockSize:2*blockSize])
	s.assert.EqualValues(data, output[2*blockSize:3*blockSize])
}

func (s *datalakeTestSuite) TestFlushFileAppendAndTruncateBlocksChunkedFile() {
	defer s.cleanupTest()

	// Setup
	name := generateFileName()
	blockSize := 7 * MB
	fileSize := 16 * MB
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	data := make([]byte, fileSize)
	rand.Read(data)

	// use our method to make the max upload size (size before a blob is broken down to blocks) to 4 Bytes
	err := uploadReaderAtToBlockBlob(ctx, bytes.NewReader(data), int64(len(data)), 4,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name), &blockblob.UploadBufferOptions{
			BlockSize: int64(blockSize),
		})
	s.assert.Nil(err)
	bol, _ := s.az.GetFileBlockOffsets(internal.GetFileBlockOffsetsOptions{Name: name})
	handlemap.CreateCacheObject(int64(16*MB), h)
	h.CacheObj.BlockOffsetList = bol
	h.CacheObj.BlockIdLength = 16

	data1 := make([]byte, blockSize)
	rand.Read(data1)
	blk1 := &common.Block{
		StartIndex: int64(fileSize),
		EndIndex:   int64(fileSize + blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
		Data:       data1,
	}
	blk1.Flags.Set(common.DirtyBlock)

	blk2 := &common.Block{
		StartIndex: int64(fileSize + blockSize),
		EndIndex:   int64(fileSize + 2*blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk2.Flags.Set(common.DirtyBlock)
	blk2.Flags.Set(common.TruncatedBlock)

	blk3 := &common.Block{
		StartIndex: int64(fileSize + 2*blockSize),
		EndIndex:   int64(fileSize + 3*blockSize),
		Id:         base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(h.CacheObj.BlockIdLength)),
	}
	blk3.Flags.Set(common.DirtyBlock)
	blk3.Flags.Set(common.TruncatedBlock)
	h.CacheObj.BlockOffsetList.BlockList = append(h.CacheObj.BlockOffsetList.BlockList, blk1, blk2, blk3)
	bol.Flags.Clear(common.SmallFile)

	err = s.az.FlushFile(internal.FlushFileOptions{Handle: h})
	s.assert.Nil(err)

	// file should be empty
	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
	s.assert.Nil(err)
	s.assert.EqualValues(data, output[:fileSize])
	emptyData := make([]byte, blockSize)
	s.assert.EqualValues(blk1.Data, output[fileSize:fileSize+blockSize])
	s.assert.EqualValues(emptyData, output[fileSize+blockSize:fileSize+2*blockSize])
	s.assert.EqualValues(emptyData, output[fileSize+2*blockSize:fileSize+3*blockSize])
}

func (s *datalakeTestSuite) TestUpdateConfig() {
	defer s.cleanupTest()

	s.az.storage.UpdateConfig(AzStorageConfig{
		blockSize:             7 * MB,
		maxConcurrency:        4,
		defaultTier:           to.Ptr(blob.AccessTierArchive),
		ignoreAccessModifiers: true,
	})

	s.assert.EqualValues(7*MB, s.az.storage.(*Datalake).Config.blockSize)
	s.assert.EqualValues(4, s.az.storage.(*Datalake).Config.maxConcurrency)
	s.assert.EqualValues(blob.AccessTierArchive, *s.az.storage.(*Datalake).Config.defaultTier)
	s.assert.True(s.az.storage.(*Datalake).Config.ignoreAccessModifiers)

	s.assert.EqualValues(7*MB, s.az.storage.(*Datalake).BlockBlob.Config.blockSize)
	s.assert.EqualValues(4, s.az.storage.(*Datalake).BlockBlob.Config.maxConcurrency)
	s.assert.EqualValues(blob.AccessTierArchive, *s.az.storage.(*Datalake).BlockBlob.Config.defaultTier)
	s.assert.True(s.az.storage.(*Datalake).BlockBlob.Config.ignoreAccessModifiers)
}

func (s *datalakeTestSuite) TestDownloadWithCPKEnabled() {
	defer s.cleanupTest()
	s.tearDownTestHelper(false)
	CPKEncryptionKey, CPKEncryptionKeySHA256 := generateCPKInfo()

	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  cpk-enabled: true\n  cpk-encryption-key: %s\n  cpk-encryption-key-sha256: %s\n",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container, CPKEncryptionKey, CPKEncryptionKeySHA256)
	s.setupTestHelper(config, s.container, false)

	blobCPKOpt := &blob.CPKInfo{
		EncryptionKey:       &CPKEncryptionKey,
		EncryptionKeySHA256: &CPKEncryptionKeySHA256,
		EncryptionAlgorithm: to.Ptr(blob.EncryptionAlgorithmTypeAES256),
	}
	name := generateFileName()
	s.az.CreateFile(internal.CreateFileOptions{Name: name})
	testData := "test data"
	data := []byte(testData)

	err := uploadReaderAtToBlockBlob(
		ctx, bytes.NewReader(data),
		int64(len(data)),
		100,
		s.az.storage.(*Datalake).BlockBlob.Container.NewBlockBlobClient(name),
		&blockblob.UploadBufferOptions{
			CPKInfo: blobCPKOpt,
		})
	s.assert.Nil(err)

	f, err := os.Create(name)
	s.assert.Nil(err)
	s.assert.NotNil(f)

	err = s.az.storage.ReadToFile(name, 0, int64(len(data)), f)
	s.assert.Nil(err)
	fileData, err := os.ReadFile(name)
	s.assert.Nil(err)
	s.assert.EqualValues(data, fileData)

	buf := make([]byte, len(data))
	err = s.az.storage.ReadInBuffer(name, 0, int64(len(data)), buf, nil)
	s.assert.Nil(err)
	s.assert.EqualValues(data, buf)

	rbuf, err := s.az.storage.ReadBuffer(name, 0, int64(len(data)))
	s.assert.Nil(err)
	s.assert.EqualValues(data, rbuf)
	_ = s.az.storage.DeleteFile(name)
	_ = os.Remove(name)
}

func (s *datalakeTestSuite) TestUploadWithCPKEnabled() {
	defer s.cleanupTest()
	s.tearDownTestHelper(false)

	CPKEncryptionKey, CPKEncryptionKeySHA256 := generateCPKInfo()
	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  cpk-enabled: true\n  cpk-encryption-key: %s\n  cpk-encryption-key-sha256: %s\n",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container, CPKEncryptionKey, CPKEncryptionKeySHA256)
	s.setupTestHelper(config, s.container, false)

	datalakeCPKOpt := &file.CPKInfo{
		EncryptionKey:       &CPKEncryptionKey,
		EncryptionKeySHA256: &CPKEncryptionKeySHA256,
		EncryptionAlgorithm: to.Ptr(file.EncryptionAlgorithmTypeAES256),
	}

	name1 := generateFileName()
	f, err := os.Create(name1)
	s.assert.Nil(err)
	s.assert.NotNil(f)

	testData := "test data"
	data := []byte(testData)
	_, err = f.Write(data)
	s.assert.Nil(err)
	_, _ = f.Seek(0, 0)

	err = s.az.storage.WriteFromFile(name1, nil, f)
	s.assert.Nil(err)

	// Blob should have updated data
	fileClient := s.containerClient.NewFileClient(name1)
	attr, err := s.az.storage.(*Datalake).GetAttr(name1)
	s.assert.Nil(err)
	s.assert.NotNil(attr)

	resp, err := fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(len(data))},
	})
	s.assert.NotNil(err)
	s.assert.Nil(resp.RequestID)

	resp, err = fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range:   &file.HTTPRange{Offset: 0, Count: int64(len(data))},
		CPKInfo: datalakeCPKOpt,
	})
	s.assert.Nil(err)
	s.assert.NotNil(resp.RequestID)

	name2 := generateFileName()
	err = s.az.storage.WriteFromBuffer(name2, nil, data)
	s.assert.Nil(err)

	fileClient = s.containerClient.NewFileClient(name2)
	resp, err = fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: 0, Count: int64(len(data))},
	})
	s.assert.NotNil(err)
	s.assert.Nil(resp.RequestID)

	resp, err = fileClient.DownloadStream(ctx, &file.DownloadStreamOptions{
		Range:   &file.HTTPRange{Offset: 0, Count: int64(len(data))},
		CPKInfo: datalakeCPKOpt,
	})
	s.assert.Nil(err)
	s.assert.NotNil(resp.RequestID)

	_ = s.az.storage.DeleteFile(name1)
	_ = s.az.storage.DeleteFile(name2)
	_ = os.Remove(name1)
}

func getACL(dl *Datalake, name string) (string, error) {
	fileClient := dl.Filesystem.NewFileClient(filepath.Join(dl.Config.prefixPath, name))
	acl, err := fileClient.GetAccessControl(context.Background(), nil)

	if err != nil || acl.ACL == nil {
		return "", err
	}

	return *acl.ACL, nil
}

func (s *datalakeTestSuite) createFileWithData(name string, data []byte, mode os.FileMode) {
	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
	_, err := s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
	s.assert.Nil(err)

	err = s.az.Chmod(internal.ChmodOptions{Name: name, Mode: mode})
	s.assert.Nil(err)

	s.az.CloseFile(internal.CloseFileOptions{Handle: h})
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestPermissionPreservationWithoutFlag() {
	defer s.cleanupTest()
	name := generateFileName()

	data := []byte("test data")
	mode := fs.FileMode(0764)
	s.createFileWithData(name, data, mode)
	// Simulate file copy and permission checks
	_ = os.WriteFile(name+"_local", []byte("123123"), mode)
	f, err := os.OpenFile(name+"_local", os.O_RDWR, mode)
	s.assert.Nil(err)

	err = s.az.CopyFromFile(internal.CopyFromFileOptions{Name: name, File: f, Metadata: nil})
	s.assert.Nil(err)
	attr, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(attr)
	s.assert.NotEqual(os.FileMode(0764), attr.Mode)

	acl, err := getACL(s.az.storage.(*Datalake), name)
	s.assert.Nil(err)
	s.assert.Contains(acl, "user::rw-")
	s.assert.Contains(acl, "group::r--")
	s.assert.Contains(acl, "other::---")

	os.Remove(name + "_local")
}

func (s *datalakeTestSuite) TestPermissionPreservationWithFlag() {
	defer s.cleanupTest()
	// Setup
	conf := fmt.Sprintf("azstorage:\n  preserve-acl: true\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  fail-unsupported-op: true",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container)
	s.setupTestHelper(conf, s.container, false)

	name := generateFileName()
	data := []byte("test data")
	mode := fs.FileMode(0764)
	s.createFileWithData(name, data, mode)
	// Simulate file copy and permission checks
	_ = os.WriteFile(name+"_local", []byte("123123"), mode)
	f, err := os.OpenFile(name+"_local", os.O_RDWR, mode)
	s.assert.Nil(err)

	err = s.az.CopyFromFile(internal.CopyFromFileOptions{Name: name, File: f, Metadata: nil})
	s.assert.Nil(err)

	attr, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(attr)
	s.assert.Equal(os.FileMode(0764), attr.Mode)

	acl, err := getACL(s.az.storage.(*Datalake), name)
	s.assert.Nil(err)
	s.assert.Contains(acl, "user::rwx")
	s.assert.Contains(acl, "group::rw-")
	s.assert.Contains(acl, "other::r--")

	os.Remove(name + "_local")
}

func (s *datalakeTestSuite) TestPermissionPreservationWithCommit() {
	defer s.cleanupTest()
	// Setup
	s.setupTestHelper("", s.container, false)
	name := generateFileName()
	s.createFileWithData(name, []byte("test data"), fs.FileMode(0767))
	data := []byte("123123")

	id := base64.StdEncoding.EncodeToString(common.NewUUIDWithLength(16))
	err := s.az.StageData(internal.StageDataOptions{
		Name:   name,
		Id:     id,
		Data:   data,
		Offset: 0,
	})
	s.assert.Nil(err)

	ids := []string{}
	ids = append(ids, id)
	err = s.az.CommitData(internal.CommitDataOptions{
		Name:      name,
		List:      ids,
		BlockSize: 1,
	})
	s.assert.Nil(err)

	attr, err := s.az.GetAttr(internal.GetAttrOptions{Name: name})
	s.assert.Nil(err)
	s.assert.NotNil(attr)
	s.assert.EqualValues(os.FileMode(0767), attr.Mode)

	acl, err := getACL(s.az.storage.(*Datalake), name)
	s.assert.Nil(err)
	s.assert.Contains(acl, "user::rwx")
	s.assert.Contains(acl, "group::rw-")
	s.assert.Contains(acl, "other::rwx")
}

func (s *datalakeTestSuite) TestBlobFilters() {
	defer s.cleanupTest()
	// Setup
	var err error
	name := generateDirectoryName()
	err = s.az.CreateDir(internal.CreateDirOptions{Name: name})
	s.assert.Nil(err)
	_, err = s.az.CreateFile(internal.CreateFileOptions{Name: name + "/abcd1.txt"})
	s.assert.Nil(err)
	_, err = s.az.CreateFile(internal.CreateFileOptions{Name: name + "/abcd2.txt"})
	s.assert.Nil(err)
	_, err = s.az.CreateFile(internal.CreateFileOptions{Name: name + "/abcd3.txt"})
	s.assert.Nil(err)
	_, err = s.az.CreateFile(internal.CreateFileOptions{Name: name + "/abcd4.txt"})
	s.assert.Nil(err)
	_, err = s.az.CreateFile(internal.CreateFileOptions{Name: name + "/bcd1.txt"})
	s.assert.Nil(err)
	_, err = s.az.CreateFile(internal.CreateFileOptions{Name: name + "/cd1.txt"})
	s.assert.Nil(err)
	_, err = s.az.CreateFile(internal.CreateFileOptions{Name: name + "/d1.txt"})
	s.assert.Nil(err)
	err = s.az.CreateDir(internal.CreateDirOptions{Name: name + "/subdir"})
	s.assert.Nil(err)

	var iteration int = 0
	var marker string = ""
	blobList := make([]*internal.ObjAttr, 0)

	for {
		new_list, new_marker, err := s.az.StreamDir(internal.StreamDirOptions{Name: name + "/", Token: marker, Count: 50})
		s.assert.Nil(err)
		blobList = append(blobList, new_list...)
		marker = new_marker
		iteration++

		log.Debug("AzStorage::ReadDir : So far retrieved %d objects in %d iterations", len(blobList), iteration)
		if new_marker == "" {
			break
		}
	}
	s.assert.EqualValues(8, len(blobList))
	err = s.az.storage.(*Datalake).SetFilter("name=^abcd.*")
	s.assert.Nil(err)

	blobList = make([]*internal.ObjAttr, 0)
	for {
		new_list, new_marker, err := s.az.StreamDir(internal.StreamDirOptions{Name: name + "/", Token: marker, Count: 50})
		s.assert.Nil(err)
		blobList = append(blobList, new_list...)
		marker = new_marker
		iteration++

		log.Debug("AzStorage::ReadDir : So far retrieved %d objects in %d iterations", len(blobList), iteration)
		if new_marker == "" {
			break
		}
	}

	s.assert.EqualValues(5, len(blobList))
	err = s.az.storage.(*Datalake).SetFilter("name=^bla.*")
	s.assert.Nil(err)

	blobList = make([]*internal.ObjAttr, 0)
	for {
		new_list, new_marker, err := s.az.StreamDir(internal.StreamDirOptions{Name: name + "/", Token: marker, Count: 50})
		s.assert.Nil(err)
		blobList = append(blobList, new_list...)
		marker = new_marker
		iteration++

		log.Debug("AzStorage::ReadDir : So far retrieved %d objects in %d iterations", len(blobList), iteration)
		if new_marker == "" {
			break
		}
	}

	s.assert.EqualValues(1, len(blobList))
	err = s.az.storage.(*Datalake).SetFilter("")
	s.assert.Nil(err)
}

func (s *datalakeTestSuite) TestList() {
	defer s.cleanupTest()
	// Setup
	s.tearDownTestHelper(false) // Don't delete the generated container.
	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  endpoint: https://%s.dfs.core.windows.net/\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n",
		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container)
	s.setupTestHelper(config, s.container, false)

	base := generateDirectoryName()
	s.setupHierarchy(base)

	blobList, marker, err := s.az.storage.List(base, nil, 0)
	s.assert.Nil(err)
	emptyString := ""
	s.assert.Equal(&emptyString, marker)
	s.assert.NotNil(blobList)
	s.assert.EqualValues(3, len(blobList))
	s.assert.NotEqual(0, blobList[0].Mode)

	// Test listing with prefix
	blobList, marker, err = s.az.storage.List(base+"b/", nil, 0)
	s.assert.Nil(err)
	s.assert.Equal(&emptyString, marker)
	s.assert.NotNil(blobList)
	s.assert.EqualValues(1, len(blobList))
	s.assert.EqualValues("c1", blobList[0].Name)
	s.assert.NotEqual(0, blobList[0].Mode)

	// Test listing with marker
	blobList, marker, err = s.az.storage.List(base, to.Ptr("invalid-marker"), 0)
	s.assert.NotNil(err)
	s.assert.Equal(0, len(blobList))
	s.assert.Nil(marker)

	// Test listing with count
	blobList, marker, err = s.az.storage.List("", nil, 1)
	s.assert.Nil(err)
	s.assert.NotNil(blobList)
	s.assert.NotEmpty(marker)
	s.assert.EqualValues(1, len(blobList))
	s.assert.EqualValues(base, blobList[0].Path)
	s.assert.NotEqual(0, blobList[0].Mode)
}

// func (s *datalakeTestSuite) TestRAGRS() {
// 	defer s.cleanupTest()
// 	// Setup
// 	name := generateFileName()
// 	h, _ := s.az.CreateFile(internal.CreateFileOptions{Name: name})
// 	testData := "test data"
// 	data := []byte(testData)
// 	s.az.WriteFile(internal.WriteFileOptions{Handle: h, Offset: 0, Data: data})
// 	h, _ = s.az.OpenFile(internal.OpenFileOptions{Name: name})
// 	s.az.CloseFile(internal.CloseFileOptions{Handle: h})

// 	// This can be flaky since it may take time to replicate the data. We could hardcode a container and file for this test
// 	time.Sleep(time.Second * time.Duration(10))

// 	s.tearDownTestHelper(false) // Don't delete the generated container.

// 	config := fmt.Sprintf("azstorage:\n  account-name: %s\n  type: adls\n  account-key: %s\n  mode: key\n  container: %s\n  endpoint: https://%s-secondary.dfs.core.windows.net\n",
// 		storageTestConfigurationParameters.AdlsAccount, storageTestConfigurationParameters.AdlsKey, s.container, storageTestConfigurationParameters.AdlsAccount)
// 	s.setupTestHelper(config, s.container, false) // Don't create a new container

// 	h, _ = s.az.OpenFile(internal.OpenFileOptions{Name: name})
// 	output, err := s.az.ReadFile(internal.ReadFileOptions{Handle: h})
// 	s.assert.Nil(err)
// 	s.assert.EqualValues(testData, output)
// 	s.az.CloseFile(internal.CloseFileOptions{Handle: h})
// }

// In order for 'go test' to run this suite, we need to create
// a normal test function and pass our suite to suite.Run
func TestDatalake(t *testing.T) {
	suite.Run(t, new(datalakeTestSuite))
}
