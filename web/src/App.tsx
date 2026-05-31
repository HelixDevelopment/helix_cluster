import { BrowserRouter, Routes, Route } from 'react-router-dom';
import DashboardLayout from './layout/DashboardLayout';
import ClusterOverview from './pages/ClusterOverview';
import NodesPage from './pages/NodesPage';
import JobsPage from './pages/JobsPage';
import SessionsPage from './pages/SessionsPage';
import BuildsPage from './pages/BuildsPage';
import HealthPage from './pages/HealthPage';
import SecurityPage from './pages/SecurityPage';

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<DashboardLayout />}>
          <Route index element={<ClusterOverview />} />
          <Route path="nodes" element={<NodesPage />} />
          <Route path="jobs" element={<JobsPage />} />
          <Route path="sessions" element={<SessionsPage />} />
          <Route path="builds" element={<BuildsPage />} />
          <Route path="health" element={<HealthPage />} />
          <Route path="security" element={<SecurityPage />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}

export default App;
