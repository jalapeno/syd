import { useState, useCallback } from 'react';
import Sidebar from './components/Sidebar';
import TopologyCanvas from './components/TopologyCanvas';
import Popover from './components/Popover';
import PathRequestPanel from './components/PathRequestPanel';
import type { GraphNode, GraphLink, PathResponse } from './types/api';

export type SidebarView = 'menu' | 'detail' | 'paths' | 'workloads' | 'endpoints';

export interface PopoverData {
  x: number;
  y: number;
  type: 'node' | 'link';
  data: GraphNode | GraphLink;
}

function App() {
  const [selectedNodes, setSelectedNodes] = useState<GraphNode[]>([]);
  const [sidebarView, setSidebarView] = useState<SidebarView>('menu');
  const [detailData, setDetailData] = useState<GraphNode | GraphLink | null>(null);
  const [popover, setPopover] = useState<PopoverData | null>(null);
  const [pathResponse, setPathResponse] = useState<PathResponse | null>(null);
  const [topologyId, setTopologyId] = useState<string>('');

  const handleNodeClick = useCallback((node: GraphNode, multiSelect: boolean) => {
    setSelectedNodes((prev) => {
      if (multiSelect) {
        const exists = prev.find((n) => n.id === node.id);
        if (exists) return prev.filter((n) => n.id !== node.id);
        return [...prev, node];
      }
      return [node];
    });
    setDetailData(node);
    setSidebarView('detail');
    setPopover(null);
  }, []);

  const handleLinkClick = useCallback((link: GraphLink) => {
    setDetailData(link);
    setSidebarView('detail');
    setPopover(null);
  }, []);

  const handleHover = useCallback((data: PopoverData | null) => {
    setPopover(data);
  }, []);

  const handleCanvasClick = useCallback(() => {
    setPopover(null);
  }, []);

  const handlePathResponse = useCallback((resp: PathResponse) => {
    setPathResponse(resp);
    setSidebarView('paths');
  }, []);

  const handleClearSelection = useCallback(() => {
    setSelectedNodes([]);
    setDetailData(null);
    setSidebarView('menu');
  }, []);

  const handleClearPaths = useCallback(() => {
    setPathResponse(null);
    setSelectedNodes([]);
    setSidebarView('menu');
  }, []);

  const handleSelectionChange = useCallback((nodes: GraphNode[]) => {
    setSelectedNodes(nodes);
  }, []);

  return (
    <div className="flex h-full">
      <Sidebar
        view={sidebarView}
        onViewChange={setSidebarView}
        detailData={detailData}
        selectedNodes={selectedNodes}
        pathResponse={pathResponse}
        topologyId={topologyId}
        onTopologyChange={setTopologyId}
        onClearSelection={handleClearSelection}
        onClearPaths={handleClearPaths}
        onPathResponse={handlePathResponse}
        onSelectionChange={handleSelectionChange}
      />
      <main className="flex-1 relative overflow-hidden">
        <TopologyCanvas
          topologyId={topologyId}
          selectedNodes={selectedNodes}
          pathResponse={pathResponse}
          onNodeClick={handleNodeClick}
          onLinkClick={handleLinkClick}
          onHover={handleHover}
          onCanvasClick={handleCanvasClick}
        />
        {popover && <Popover data={popover} />}
        {selectedNodes.length >= 2 && sidebarView !== 'paths' && (
          <PathRequestPanel
            topologyId={topologyId}
            selectedNodes={selectedNodes}
            onPathResponse={handlePathResponse}
          />
        )}
      </main>
    </div>
  );
}

export default App;
