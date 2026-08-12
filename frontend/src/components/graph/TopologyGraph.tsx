import { FC, useEffect, useRef } from 'react';
import Graph from 'graphology';
import Sigma from 'sigma';

export interface GraphNode {
  id: string;
  label: string;
  x?: number;
  y?: number;
  size?: number;
  color?: string;
}

export interface GraphEdge {
  source: string;
  target: string;
  label?: string;
}

export interface TopologyGraphProps {
  nodes: GraphNode[];
  edges: GraphEdge[];
  /** Nodes in the set are dimmed (e.g. predicted outage impact). */
  dimmedNodes?: ReadonlySet<string>;
  /** Nodes in the set are emphasized with a red halo (e.g. failed CI). */
  highlightedNodes?: ReadonlySet<string>;
  className?: string;
}

const DIMMED_COLOR = '#d1d5db';
const DIMMED_EDGE_COLOR = '#e5e7eb';
const HIGHLIGHT_COLOR = '#dc2626';

/**
 * TopologyGraph renders an interactive graph of CI relationships
 * using Sigma.js backed by a graphology instance.
 */
export const TopologyGraph: FC<TopologyGraphProps> = ({
  nodes,
  edges,
  dimmedNodes,
  highlightedNodes,
  className,
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const sigmaRef = useRef<Sigma | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const graph = new Graph();

    nodes.forEach((node, i) => {
      graph.addNode(node.id, {
        label: node.label,
        x: node.x ?? Math.cos((2 * Math.PI * i) / nodes.length),
        y: node.y ?? Math.sin((2 * Math.PI * i) / nodes.length),
        size: node.size ?? 10,
        color: node.color ?? '#4f46e5',
      });
    });

    edges.forEach((edge) => {
      if (graph.hasNode(edge.source) && graph.hasNode(edge.target)) {
        graph.addEdge(edge.source, edge.target, { label: edge.label });
      }
    });

    const renderer = new Sigma(graph, containerRef.current, {
      renderEdgeLabels: true,
    });

    if (dimmedNodes || highlightedNodes) {
      renderer.setSetting('nodeReducer', (node, data) => {
        const result = { ...data };
        if (highlightedNodes?.has(node)) {
          result.size = (result.size ?? 10) * 1.4;
          result.color = HIGHLIGHT_COLOR;
        } else if (dimmedNodes?.has(node)) {
          result.color = DIMMED_COLOR;
        }
        return result;
      });
      renderer.setSetting('edgeReducer', (edge, data) => {
        const result = { ...data };
        const [source, target] = graph.extremities(edge);
        if (dimmedNodes?.has(source) || dimmedNodes?.has(target)) {
          result.color = DIMMED_EDGE_COLOR;
          result.label = '';
        }
        return result;
      });
    }

    sigmaRef.current = renderer;

    return () => {
      renderer.kill();
      sigmaRef.current = null;
    };
  }, [nodes, edges, dimmedNodes, highlightedNodes]);

  return <div ref={containerRef} className={className} style={{ width: '100%', height: '100%' }} />;
};
