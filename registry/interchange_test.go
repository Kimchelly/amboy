package registry

import (
	"testing"
	"time"

	"github.com/evergreen-ci/utility"
	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/dependency"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// JobInterchangeSuite tests the JobInterchange format and
// converters. JobInterchange provides a generic method, in
// conjunction with the job type registry to let amboy be able to
// serialize and pass job data between instances. This is used both to
// pass information between different queue instances *and* by the
// JobGroup implementation.
type JobInterchangeSuite struct {
	job *JobTest
	suite.Suite
	format amboy.Format
}

func TestJobInterchangeSuiteJSON(t *testing.T) {
	s := new(JobInterchangeSuite)
	s.format = amboy.JSON
	suite.Run(t, s)
}

func TestJobInterchangeSuiteLegacyBSON(t *testing.T) {
	s := new(JobInterchangeSuite)
	s.format = amboy.BSON
	suite.Run(t, s)
}

func TestJobInterchangeSuiteBSON(t *testing.T) {
	s := new(JobInterchangeSuite)
	s.format = amboy.BSON2
	suite.Run(t, s)
}

func (s *JobInterchangeSuite) SetupTest() {
	s.job = NewTestJob("interchange-test")
}

func (s *JobInterchangeSuite) TestRoundTripHighLevel() {
	s.Equal("always", s.job.Dependency().Type().Name)

	i, err := MakeJobInterchange(s.job, s.format)
	s.Require().NoError(err)

	outJob, err := i.Resolve(s.format)
	s.NoError(err)

	if s.format == amboy.BSON {
		s.Equal(s.job.Dependency().Type().Name, outJob.Dependency().Type().Name)
		// Decoding the job from BSON sets the scopes to the empty slice instead of
		// a nil slice.
		outJob.SetScopes(nil)
		// mgo/bson seems to unset/nil the private map in the
		// implementation of the dependency. It's not material
		// to this test so we fake it out
		outJob.SetDependency(s.job.Dependency())
	}

	s.Equal(s.job, outJob)
}

func (s *JobInterchangeSuite) TestConversionToInterchangeMaintainsMetaDataFidelity() {
	i, err := MakeJobInterchange(s.job, s.format)
	if s.NoError(err) {
		s.Equal(s.job.ID(), i.Name)
		s.Equal(s.job.Type().Name, i.Type)
		s.Equal(s.job.Type().Version, i.Version)
		s.Equal(s.job.Status(), i.Status)
	}
}

func (s *JobInterchangeSuite) TestConversionToInterchangeTruncatesUnreasonablyLongErrorsInMetaData() {
	const numErrs = 5000
	allErrors := make([]string, 0, numErrs)
	for i := 0; i < numErrs; i++ {
		allErrors = append(allErrors, utility.MakeRandomString(10000))
	}
	for _, err := range allErrors {
		s.job.AddError(errors.New(err))
	}
	i, err := MakeJobInterchange(s.job, s.format)
	s.NoError(err)
	s.Greater(len(allErrors), len(i.Status.Errors), "if jobs has too many errors, it should truncate some of them down to a reasonable amount")

	interchangeStatusWithoutErrs := i.Status
	interchangeStatusWithoutErrs.Errors = nil
	jobStatusWithoutErrs := s.job.Status()
	jobStatusWithoutErrs.Errors = nil
	s.Equal(jobStatusWithoutErrs, interchangeStatusWithoutErrs, "all other status fields except long errors should be maintained")
}

func (s *JobInterchangeSuite) TestConversionFromInterchangeMaintainsFidelity() {
	i, err := MakeJobInterchange(s.job, s.format)
	if !s.NoError(err) {
		return
	}

	j, err := i.Resolve(s.format)

	if s.NoError(err) {
		s.IsType(s.job, j)

		new := j.(*JobTest)

		s.Equal(s.job.Name, new.Name)
		s.Equal(s.job.Content, new.Content)
		s.Equal(s.job.ShouldFail, new.ShouldFail)
		s.Equal(s.job.T, new.T)
	}
}

func (s *JobInterchangeSuite) TestUnregisteredTypeCannotConvertToJob() {
	s.job.T.Name = "different"

	i, err := MakeJobInterchange(s.job, s.format)
	if s.NoError(err) {
		j, err := i.Resolve(s.format)
		s.Nil(j)
		s.Error(err)
	}
}

func (s *JobInterchangeSuite) TestMismatchedVersionResultsInErrorOnConversion() {
	s.job.T.Version += 100

	i, err := MakeJobInterchange(s.job, s.format)
	if s.NoError(err) {
		j, err := i.Resolve(s.format)
		s.Nil(j)
		s.Error(err)
	}
}

func (s *JobInterchangeSuite) TestConvertToJobForUnknownJobType() {
	s.job.T.Name = "missing-job-type"
	i, err := MakeJobInterchange(s.job, s.format)
	if s.NoError(err) {
		j, err := i.Resolve(s.format)
		s.Nil(j)
		s.Error(err)
	}
}

func (s *JobInterchangeSuite) TestMismatchedDependencyCausesJobConversionToError() {
	s.job.T.Version += 100

	i, err := MakeJobInterchange(s.job, s.format)
	if s.NoError(err) {
		j, err := i.Resolve(s.format)
		s.Error(err)
		s.Nil(j)
	}
}

func (s *JobInterchangeSuite) TestTimeInfoPersists() {
	now := time.Now()
	ti := amboy.JobTimeInfo{
		Start:     now.Round(time.Millisecond),
		End:       now.Add(time.Hour).Round(time.Millisecond),
		WaitUntil: now.Add(-time.Minute).Round(time.Millisecond),
	}
	s.job.UpdateTimeInfo(ti)
	s.Equal(ti, s.job.TimingInfo)

	i, err := MakeJobInterchange(s.job, s.format)
	if s.NoError(err) {
		s.Equal(i.TimeInfo, ti)

		j, err := i.Resolve(s.format)
		s.NoError(err)
		if s.NotNil(j) {
			s.Equal(ti, j.TimeInfo())
		}
	}
}

func (s *JobInterchangeSuite) TestEnqueueScopesPersists() {
	scopes := []string{"foo", "bar"}
	s.job.SetScopes(scopes)
	s.job.SetEnqueueScopes(scopes...)

	i, err := MakeJobInterchange(s.job, s.format)
	s.Require().NoError(err)

	j, err := i.Resolve(s.format)
	s.Require().NoError(err)
	s.Equal(scopes, j.EnqueueScopes())
}

func (s *JobInterchangeSuite) TestEnqueueAllScopesPersists() {
	s.job.SetEnqueueAllScopes(true)

	i, err := MakeJobInterchange(s.job, s.format)
	s.Require().NoError(err)

	j, err := i.Resolve(s.format)
	s.Require().NoError(err)
	s.True(j.EnqueueAllScopes())
}

func (s *JobInterchangeSuite) TestRetryInfoPersists() {
	info := amboy.JobRetryInfo{
		Retryable:      true,
		CurrentAttempt: 5,
	}
	s.job.UpdateRetryInfo(info.Options())
	s.Equal(info, s.job.RetryInfo())

	ji, err := MakeJobInterchange(s.job, s.format)
	s.Require().NoError(err)
	s.Equal(info, ji.RetryInfo)
}

// DependencyInterchangeSuite tests the DependencyInterchange format
// and converters. This type provides a way for Jobs and Queues to
// serialize their objects quasi-generically.
type DependencyInterchangeSuite struct {
	dep         dependency.Manager
	interchange *DependencyInterchange
	require     *require.Assertions
	suite.Suite
}

func TestDependencyInterchangeSuite(t *testing.T) {
	suite.Run(t, new(DependencyInterchangeSuite))
}

func (s *DependencyInterchangeSuite) SetupSuite() {
	s.require = s.Require()
}

func (s *DependencyInterchangeSuite) SetupTest() {
	s.dep = dependency.NewAlways()
	s.Equal(s.dep.Type().Name, "always")
}

func (s *DependencyInterchangeSuite) TestDependencyInterchangeFormatStoresDataCorrectly() {
	i, err := makeDependencyInterchange(amboy.BSON, s.dep)
	if s.NoError(err) {
		s.Equal(s.dep.Type().Name, i.Type)
		s.Equal(s.dep.Type().Version, i.Version)
	}
}

func (s *DependencyInterchangeSuite) TestConvertFromDependencyInterchangeFormatMaintainsFidelity() {
	i, err := makeDependencyInterchange(amboy.BSON, s.dep)
	if s.NoError(err) {
		s.require.IsType(i, s.interchange)

		dep, err := convertToDependency(amboy.BSON, i)
		s.NoError(err)
		s.require.IsType(dep, s.dep)
		old := s.dep
		new := dep
		s.Equal(old.Type(), new.Type())
		s.Equal(len(old.Edges()), len(new.Edges()))
	}

}

func (s *DependencyInterchangeSuite) TestVersionInconsistencyCauseConverstionToDependencyToError() {
	i, err := makeDependencyInterchange(amboy.BSON, s.dep)
	if s.NoError(err) {
		s.require.IsType(i, s.interchange)

		i.Version += 100

		dep, err := convertToDependency(amboy.BSON, i)
		s.Error(err)
		s.Nil(dep)
	}
}

func (s *DependencyInterchangeSuite) TestNameInconsistencyCasuesConversionToDependencyToError() {
	i, err := makeDependencyInterchange(amboy.BSON, s.dep)
	if s.NoError(err) {
		s.require.IsType(i, s.interchange)

		i.Type = "sommetimes"

		dep, err := convertToDependency(amboy.BSON, i)
		s.Error(err)
		s.Nil(dep)
	}
}
